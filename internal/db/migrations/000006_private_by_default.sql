-- 000006_private_by_default.sql — new shipments are private by default.
--
-- WHY: is_public DEFAULT true leaked every new shipment to the unauthenticated
-- portal (enumeration + precise lat/lng). Unicorn contract: explicit publish.
-- Existing rows keep their value; only the default flips. Demo seed sets
-- is_public explicitly so the public demo still works.

ALTER TABLE shipments ALTER COLUMN is_public SET DEFAULT false;
