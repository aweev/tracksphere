-- 000016_exception_engine.sql — make exceptions workable and detection
-- time-based.
--
-- WHY THIS EXISTS
--
-- The exception queue shipped as a list with a resolve button. Three structural
-- gaps made it unusable as an operations tool:
--
--   1. No ownership. An alert with no assignee belongs to nobody, so in a team
--      nobody works it. Unowned work is invisible work.
--   2. No time-based detection. Every rule was event-triggered, so the most
--      expensive exception in freight — *nothing happening* — was invisible.
--      A container sitting at a terminal for six days generates no event, so
--      no rule fired, forever.
--   3. No reconciliation. Alerts were only ever appended. The SLA-breach alert
--      opened on day 1 of a late shipment stayed open at day 90 even after the
--      container was delivered, poisoning every "open exceptions" number.
--
-- This migration adds the state those three fixes need. It is deliberately
-- additive: every column is nullable or defaulted, so no existing row changes
-- meaning and no handler breaks.

-- ── 1. Exception lifecycle ───────────────────────────────────────────────
--
-- The kind taxonomy grows here. Event-driven kinds come from rules.go;
-- time-driven kinds are produced by the sweep (ADR 0007) and can only be
-- raised there, never from an event handler. Keeping them in one closed set
-- means the "was this found by a clock or by a webhook" question is always
-- answerable by looking at the kind.
ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN (
    -- event-driven
    'delay', 'customs_hold', 'port_congestion', 'sla_breach', 'eta_revised',
    'disruption', 'delivery_failed',
    -- time-driven (sweep only)
    'stale', 'dwell', 'dwell_critical', 'eta_slip', 'recurring'));

--
-- detected_at  when the condition was first true
-- last_seen_at the sweep most recently confirmed it still is (stale alerts can
--              be distinguished from live ones without closing them)
-- acknowledged_at / assigned_to / due_at  the ownership loop
-- snoozed_until  defer without pretending it does not exist
-- escalated_at  when the SLA was breached and someone was told
-- resolved_*    how it closed, and who closed it
ALTER TABLE alerts
    ADD COLUMN IF NOT EXISTS detected_at      timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS last_seen_at     timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS acknowledged_at  timestamptz,
    ADD COLUMN IF NOT EXISTS assigned_to      uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS assigned_at      timestamptz,
    ADD COLUMN IF NOT EXISTS due_at           timestamptz,
    ADD COLUMN IF NOT EXISTS snoozed_until    timestamptz,
    ADD COLUMN IF NOT EXISTS escalated_at     timestamptz,
    ADD COLUMN IF NOT EXISTS root_cause       text,
    ADD COLUMN IF NOT EXISTS note             text,
    ADD COLUMN IF NOT EXISTS resolved_at      timestamptz,
    ADD COLUMN IF NOT EXISTS resolved_by      uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS resolution       text,
    ADD COLUMN IF NOT EXISTS value_at_risk    numeric(12,2);

-- Root-cause taxonomy is closed so carrier scorecards stay aggregatable.
ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_root_cause_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_root_cause_check CHECK (root_cause IS NULL OR root_cause IN (
    'documentation', 'customs_duty', 'customs_processing', 'carrier_delay',
    'port_congestion', 'weather', 'capacity', 'missed_connection',
    'blank_sailing', 'carrier_error', 'shipper_delay', 'other'));

-- Resolution must say WHY it closed, so auto-clears are distinguishable from
-- human decisions in reporting.
ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_resolution_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_resolution_check CHECK (resolution IS NULL OR resolution IN (
    'condition_cleared', 'customer_notified', 'carrier_contacted',
    'documents_filed', 'delivered', 'duplicate', 'false_positive', 'wont_fix'));

-- SLA targets per severity (minutes). 24h/4h/1h mirrors the escalation policy:
-- critical pages inside the hour, warnings inside the shift, info is never
-- escalated because info never interrupts (see notification_prefs).
ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS sla_critical_minutes integer NOT NULL DEFAULT 60,
    ADD COLUMN IF NOT EXISTS sla_warning_minutes  integer NOT NULL DEFAULT 240,
    ADD COLUMN IF NOT EXISTS sla_info_minutes     integer NOT NULL DEFAULT 1440,
    ADD COLUMN IF NOT EXISTS timezone            text NOT NULL DEFAULT 'UTC';

-- The queue is read as "my open work, worst first, soonest due first".
CREATE INDEX IF NOT EXISTS alerts_queue_idx
    ON alerts (tenant_id, status, snoozed_until, due_at)
    WHERE status = 'open';
CREATE INDEX IF NOT EXISTS alerts_severity_idx
    ON alerts (tenant_id, severity, detected_at DESC)
    WHERE status = 'open';
CREATE INDEX IF NOT EXISTS alerts_shipment_idx ON alerts (shipment_id) WHERE status = 'open';
-- Reconciliation and escalation sweeps scan by status + staleness.
CREATE INDEX IF NOT EXISTS alerts_detected_idx ON alerts (tenant_id, detected_at DESC);

-- ── 2. Milestones: the missing expressiveness ─────────────────────────────
--
-- shipments.status is one flat mutable field, so there is nowhere to record
-- "arrived at Rotterdam 6 days ago". Without per-milestone timestamps, dwell
-- time cannot be measured, and without dwell time the highest-value exception
-- class in freight cannot be detected.
CREATE TABLE IF NOT EXISTS shipment_milestones (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id   uuid NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
    code          text NOT NULL CHECK (code IN (
                      'BOOKED', 'DEPARTED_ORIGIN', 'ARRIVED_TRANSIT',
                      'CUSTOMS_ENTRY', 'CUSTOMS_CLEARED', 'ARRIVED_DESTINATION',
                      'OUT_FOR_DELIVERY', 'DELIVERED')),
    location      text,
    expected_at   timestamptz,
    actual_at     timestamptz,
    dwell_hours   numeric(10,2),
    source        text NOT NULL DEFAULT 'carrier',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS shipment_milestones_uniq
    ON shipment_milestones (shipment_id, code);
CREATE INDEX IF NOT EXISTS shipment_milestones_tenant_idx
    ON shipment_milestones (tenant_id, shipment_id);

ALTER TABLE shipment_milestones ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_milestones FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS shipment_milestones_isolation ON shipment_milestones;
CREATE POLICY shipment_milestones_isolation ON shipment_milestones
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- ── 3. shipment_current read model ───────────────────────────────────────
--
-- Every list view and dashboard tile was a live aggregate over the write
-- tables: five independent counts on the dashboard, count(*) plus a page on the
-- list, and a correlated max(occurred_at) subquery per row for the poller.
-- That is fine at six shipments (the seed data) and unacceptable at 50,000.
--
-- shipment_current is one narrow, index-friendly row per shipment carrying the
-- fields every hot path reads. It is what makes risk-sorting possible at all:
-- the score is computed once, here, instead of per request in the UI.
CREATE TABLE IF NOT EXISTS shipment_current (
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id      uuid PRIMARY KEY REFERENCES shipments(id) ON DELETE CASCADE,
    tracking_number  text NOT NULL,
    carrier          text NOT NULL DEFAULT '',
    mode             text NOT NULL DEFAULT '',
    origin           text NOT NULL DEFAULT '',
    destination      text NOT NULL DEFAULT '',
    status           text NOT NULL DEFAULT 'booked',
    is_public        boolean NOT NULL DEFAULT false,
    -- Risk: 0-100, higher is worse. Tiered for colour/priority without magic
    -- numbers in the client.
    risk_score       integer NOT NULL DEFAULT 0,
    risk_tier        text NOT NULL DEFAULT 'clear'
                     CHECK (risk_tier IN ('clear', 'watch', 'at_risk', 'critical')),
    -- Exception rollup. Denormalised so the list view never joins alerts.
    open_alerts      integer NOT NULL DEFAULT 0,
    critical_alerts  integer NOT NULL DEFAULT 0,
    -- Freshness. Staleness is the single most valuable derived number in the
    -- product: "no scan in 48h" is the exception nobody can report by eye.
    last_event_at    timestamptz,
    last_event_code  text,
    stale_hours      numeric(10,2),
    -- ETA with provenance. A carrier-published ETA and our own estimate must
    -- never render identically, or operators stop believing every number.
    eta              timestamptz,
    eta_source       text NOT NULL DEFAULT 'none'
                     CHECK (eta_source IN ('none', 'carrier', 'estimated', 'lane_model')),
    eta_confidence   numeric(5,4),
    eta_slip_hours   numeric(10,2),
    -- Dwell against the lane norm.
    dwell_hours      numeric(10,2),
    expected_dwell_hours numeric(10,2),
    dwell_ratio      numeric(10,4),
    -- Business impact, surfaced on the exception card ("$1,240 at risk").
    value_at_risk    numeric(12,2),
    customer_notified boolean NOT NULL DEFAULT false,
    shipped_at       timestamptz,
    delivered_at     timestamptz,
    on_time          boolean,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS shipment_current_risk_idx
    ON shipment_current (tenant_id, risk_score DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS shipment_current_status_idx
    ON shipment_current (tenant_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS shipment_current_stale_idx
    ON shipment_current (tenant_id, stale_hours DESC NULLS FIRST)
    WHERE status NOT IN ('delivered', 'cancelled');
CREATE INDEX IF NOT EXISTS shipment_current_alerts_idx
    ON shipment_current (tenant_id) WHERE open_alerts > 0;
CREATE INDEX IF NOT EXISTS shipment_current_track_idx
    ON shipment_current (tenant_id, tracking_number);

ALTER TABLE shipment_current ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_current FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS shipment_current_isolation ON shipment_current;
CREATE POLICY shipment_current_isolation ON shipment_current
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- ── 4. Subscription consent ──────────────────────────────────────────────
--
-- The portal accepted any recipient with no verification and no consent
-- record, then fanned out to it on every event. That made the platform an
-- unauthenticated SMS/WhatsApp relay: anyone could POST a victim's number and
-- cause unsolicited metered messages, which is harassment, a direct cost, and
-- a WhatsApp Business policy violation that risks the sending account.
--
-- Subscriptions now start 'pending' and only become deliverable after the
-- recipient confirms a token. Every step is audited.
ALTER TABLE tracking_subscriptions
    ADD COLUMN IF NOT EXISTS status        text NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'active', 'revoked', 'superseded')),
    ADD COLUMN IF NOT EXISTS confirm_token text,
    ADD COLUMN IF NOT EXISTS consent_at    timestamptz,
    ADD COLUMN IF NOT EXISTS confirmed_at  timestamptz,
    ADD COLUMN IF NOT EXISTS revoked_at    timestamptz,
    ADD COLUMN IF NOT EXISTS source        text NOT NULL DEFAULT 'portal',
    ADD COLUMN IF NOT EXISTS events        text[] NOT NULL DEFAULT '{all}',
    ADD COLUMN IF NOT EXISTS digest_only   boolean NOT NULL DEFAULT false;

-- recipient_hash is a SHA-256 fingerprint of the recipient. The cap check and
-- the opt-out must work on a value we can index and compare, and neither should
-- need to store or re-hash a plaintext address or phone number on every
-- public, unauthenticated request.
ALTER TABLE tracking_subscriptions
    ADD COLUMN IF NOT EXISTS recipient_hash text;
UPDATE tracking_subscriptions
SET recipient_hash = encode(sha256(convert_to(recipient, 'UTF8')), 'hex')
WHERE recipient_hash IS NULL;
ALTER TABLE tracking_subscriptions
    ALTER COLUMN recipient_hash SET NOT NULL;
CREATE INDEX IF NOT EXISTS tracking_subscriptions_recipient_idx
    ON tracking_subscriptions (recipient_hash);

-- The confirmation lookup is keyed on a high-entropy token and happens before
-- any tenant is known (the recipient holds no account), so it needs the system
-- branch. WITH CHECK still requires a tenant, so no un-pinned subscription can
-- be created — the escape hatch is read-only in practice.
DROP POLICY IF EXISTS tracking_subscriptions_isolation ON tracking_subscriptions;
CREATE POLICY tracking_subscriptions_isolation ON tracking_subscriptions
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

CREATE UNIQUE INDEX IF NOT EXISTS tracking_subscriptions_confirm_idx
    ON tracking_subscriptions (confirm_token) WHERE confirm_token IS NOT NULL;
-- Only active subscriptions are ever selected by the fan-out query.
CREATE INDEX IF NOT EXISTS tracking_subscriptions_active_idx
    ON tracking_subscriptions (shipment_id) WHERE status = 'active';
-- Per-shipment cap, enforced in the handler.
CREATE INDEX IF NOT EXISTS tracking_subscriptions_shipment_idx
    ON tracking_subscriptions (tenant_id, shipment_id);

CREATE TABLE IF NOT EXISTS notification_consent (
    id          bigserial PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    shipment_id uuid,
    channel     text NOT NULL CHECK (channel IN ('email', 'sms', 'whatsapp')),
    recipient   text NOT NULL,           -- retained for the compliance record
    recipient_hash text NOT NULL,        -- what the limiter and cap key on
    action      text NOT NULL CHECK (action IN
                     ('requested', 'confirmed', 'revoked', 'suppressed', 'delivered')),
    reason      text,
    ip_hash     text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notification_consent_tenant_idx
    ON notification_consent (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS notification_consent_recipient_idx
    ON notification_consent (recipient_hash, created_at DESC);

ALTER TABLE notification_consent ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_consent FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_consent_isolation ON notification_consent;
CREATE POLICY notification_consent_isolation ON notification_consent
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- ── 5. Notification routing: severity → channel, with a hard budget ──────
--
-- The single most important product constraint: an operator may never receive
-- more than N actionable interruptions per hour. Everything else is
-- channel-managed pull. This is enforced in code against the ledger below, not
-- left to policy — alert fatigue is the documented number-one reason ops teams
-- abandon visibility platforms.
CREATE TABLE IF NOT EXISTS notification_prefs (
    tenant_id           uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    timezone            text NOT NULL DEFAULT 'UTC',
    -- Quiet hours are stored in the tenant's local time, not UTC.
    quiet_hours_start   smallint NOT NULL DEFAULT 22 CHECK (quiet_hours_start BETWEEN 0 AND 23),
    quiet_hours_end     smallint NOT NULL DEFAULT 7  CHECK (quiet_hours_end   BETWEEN 0 AND 23),
    digest_hour         smallint NOT NULL DEFAULT 8  CHECK (digest_hour       BETWEEN 0 AND 23),
    -- Hard ceiling on interrupting notifications per recipient per hour.
    interrupt_hourly_cap integer NOT NULL DEFAULT 5 CHECK (interrupt_hourly_cap BETWEEN 0 AND 50),
    -- Minimum gap between two interrupts to the same shipment, so a burst of
    -- rules on one container produces one interruption, not five.
    per_shipment_cooldown_minutes integer NOT NULL DEFAULT 360,
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- The ledger the budget is enforced against. Sliding one-hour window.
CREATE TABLE IF NOT EXISTS notification_interrupts (
    id          bigserial PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    recipient_hash text NOT NULL,
    shipment_id uuid,
    alert_id    uuid,
    severity    text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notification_interrupts_window_idx
    ON notification_interrupts (recipient_hash, created_at DESC);
CREATE INDEX IF NOT EXISTS notification_interrupts_shipment_idx
    ON notification_interrupts (recipient_hash, shipment_id, created_at DESC)
    WHERE shipment_id IS NOT NULL;

-- Non-interrupting notifications queue here and are rolled into a digest.
CREATE TABLE IF NOT EXISTS notification_digest_queue (
    id          bigserial PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    recipient_hash text NOT NULL,
    shipment_id uuid,
    alert_id    uuid,
    severity    text NOT NULL,
    subject     text NOT NULL,
    body        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    flushed_at  timestamptz
);
CREATE INDEX IF NOT EXISTS notification_digest_pending_idx
    ON notification_digest_queue (tenant_id, recipient_hash)
    WHERE flushed_at IS NULL;

ALTER TABLE notification_interrupts ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_interrupts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_interrupts_isolation ON notification_interrupts;
CREATE POLICY notification_interrupts_isolation ON notification_interrupts
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE notification_digest_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_digest_queue FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_digest_queue_isolation ON notification_digest_queue;
CREATE POLICY notification_digest_queue_isolation ON notification_digest_queue
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- notification_prefs is written under the system flag by the seeding sweep.
ALTER TABLE notification_prefs ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_prefs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_prefs_isolation ON notification_prefs;
CREATE POLICY notification_prefs_isolation ON notification_prefs
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

-- ── 6. Declarative alert rules ───────────────────────────────────────────
--
-- The engine was a switch on an event code: four hardcoded rules, identical
-- for every tenant, unconfigurable. Support could not add a rule for a customer
-- without a code change and a deploy. Conditions are stored as JSON and
-- evaluated by the sweep, so rules are data.
CREATE TABLE IF NOT EXISTS alert_rules (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          text NOT NULL,
    kind          text NOT NULL,
    severity      text NOT NULL DEFAULT 'warning'
                  CHECK (severity IN ('info', 'warning', 'critical')),
    -- event = evaluated when a carrier event lands; sweep = evaluated on the
    -- clock. Staleness and dwell breaches MUST be 'sweep': they are the
    -- exceptions where no event exists at all.
    trigger_type  text NOT NULL DEFAULT 'sweep' CHECK (trigger_type IN ('event', 'sweep')),
    every_minutes integer NOT NULL DEFAULT 30 CHECK (every_minutes BETWEEN 1 AND 10080),
    scope         jsonb NOT NULL DEFAULT '{}'::jsonb,
    condition     jsonb NOT NULL DEFAULT '{}'::jsonb,
    raise_spec    jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Opt-out per rule, so support can disable a noisy rule for one customer
    -- without touching everyone else's.
    enabled       boolean NOT NULL DEFAULT true,
    version       integer NOT NULL DEFAULT 1,
    updated_by    uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS alert_rules_tenant_idx
    ON alert_rules (tenant_id, enabled, trigger_type);

ALTER TABLE alert_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_rules FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS alert_rules_isolation ON alert_rules;
CREATE POLICY alert_rules_isolation ON alert_rules
    USING (tenant_id = current_tenant()
        OR current_setting('app.system', true) = 'on')
    WITH CHECK (tenant_id = current_tenant());

-- ── 7. Scheduler lease ───────────────────────────────────────────────────
--
-- Periodic work must be single-flight across N worker replicas without any of
-- them coordinating. A lease row claimed with a conditional UPDATE gives
-- exactly one winner per window; expired leases are reclaimed automatically.
CREATE TABLE IF NOT EXISTS scheduler_leases (
    name        text PRIMARY KEY,
    holder      text NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    runs        integer NOT NULL DEFAULT 0
);

-- ── Idempotent scheduling ────────────────────────────────────────────────
--
-- The sweep and the digest flush must be schedulable from every replica and
-- every ticker without running twice. jobs had no dedup key, so a restart
-- mid-window or two replicas firing together would double-run. dedup_key is a
-- content-derived token (e.g. the sweep's hour bucket) with a partial unique
-- index, so duplicate inserts are rejected at the database.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS dedup_key text;
CREATE UNIQUE INDEX IF NOT EXISTS jobs_dedup_idx
    ON jobs (dedup_key) WHERE dedup_key IS NOT NULL;

-- Recurring jobs are pruned by ArchiveOld; keeping them around would defeat
-- the dedup key and bloat the table.
CREATE INDEX IF NOT EXISTS jobs_kind_pending_idx
    ON jobs (kind, run_at) WHERE status = 'pending';

-- slaMinutesFor(tenant, severity) is the SLA target for a severity, in minutes.
-- Lives in SQL so the INSERT that raises an alert can set due_at in the same
-- statement: a due date computed in Go and passed in would race the tenant's
-- setting and is one more thing to get wrong per call site.
CREATE OR REPLACE FUNCTION sla_minutes_for(p_tenant uuid, p_severity text)
RETURNS integer
LANGUAGE sql STABLE
AS $$
    SELECT CASE p_severity
        WHEN 'critical' THEN sla_critical_minutes
        WHEN 'warning'  THEN sla_warning_minutes
        ELSE sla_info_minutes
    END FROM tenants WHERE id = p_tenant;
$$;

-- ── Grants ───────────────────────────────────────────────────────────────
GRANT SELECT, INSERT, UPDATE, DELETE ON
    shipment_milestones, shipment_current, notification_consent,
    notification_interrupts, notification_digest_queue, notification_prefs,
    alert_rules, scheduler_leases
TO tracksphere_app;
GRANT USAGE, SELECT ON SEQUENCE notification_consent_id_seq,
    notification_interrupts_id_seq, notification_digest_queue_id_seq
TO tracksphere_app;