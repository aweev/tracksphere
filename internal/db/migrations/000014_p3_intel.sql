-- 000014_p3_intel.sql — P3 intelligence: disruption alerts, zones, SSO links.
--
-- * alerts.kind gains 'disruption' (tariff/corridor risk flags).
-- * disruption_zones is a GLOBAL curated table (no tenant): the worker
--   matches shipment origin/destination/legs against match_terms.
-- * sso_accounts pins provider subjects to users (no email-takeover after
--   first verified link).

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check
    CHECK (kind IN ('delay', 'customs_hold', 'port_congestion',
                    'sla_breach', 'eta_revised', 'disruption'));

CREATE TABLE IF NOT EXISTS disruption_zones (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    match_terms text[] NOT NULL DEFAULT '{}',
    severity    text NOT NULL DEFAULT 'warning'
                CHECK (severity IN ('info', 'warning', 'critical')),
    message     text NOT NULL DEFAULT '',
    active      boolean NOT NULL DEFAULT true
);

INSERT INTO disruption_zones (name, match_terms, severity, message) VALUES
    ('Red Sea / Suez corridor',
     '{suez,red sea,bab el-mandeb,djibouti,port said, suez canal}',
     'warning',
     'Red Sea corridor: rerouting via the Cape adds 7–14 days on Asia–Europe lanes.'),
    ('Panama Canal',
     '{panama,colon,balboa}',
     'info',
     'Panama Canal: draft restrictions can delay transits 1–3 days.'),
    ('US tariff watch',
     '{tariff,section 301,section 232,de minimis}',
     'info',
     'US tariff watch: duty changes may hold clearance — confirm HS codes.')
ON CONFLICT (name) DO NOTHING;

CREATE TABLE IF NOT EXISTS sso_accounts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider    text NOT NULL,
    subject     text NOT NULL,
    email       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, subject)
);
CREATE INDEX IF NOT EXISTS sso_accounts_user_idx ON sso_accounts (user_id);
