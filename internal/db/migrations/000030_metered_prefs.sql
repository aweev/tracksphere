-- 000030_metered_prefs.sql — tenant kill-switch for metered channels (P2-4).
--
-- SMS/WhatsApp cost money per message and carry per-country consent rules the
-- codebase cannot review for you. Default both OFF: a tenant opts in per
-- channel (future settings UI writes these flags) and every worker send path
-- checks them before touching Twilio. Missing prefs row = disabled.
ALTER TABLE notification_prefs
    ADD COLUMN IF NOT EXISTS sms_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS whatsapp_enabled boolean NOT NULL DEFAULT false;
