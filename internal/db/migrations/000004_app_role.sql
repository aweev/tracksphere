-- 000004_app_role.sql — dedicated runtime role for the application.
--
-- WHY THIS EXISTS: FORCE ROW LEVEL SECURITY applies to the table owner, but
-- a SUPERUSER bypasses RLS unconditionally. Docker's POSTGRES_USER is created
-- superuser, so connecting the app as the bootstrap user would silently
-- disable every isolation policy (this was caught by scripts/rls_test.sql).
-- The app therefore connects as tracksphere_app: no SUPERUSER, no
-- BYPASSRLS, not the table owner. Migrations keep using the owner role
-- (TRACKSPHERE_MIGRATIONS_URL).
--
-- Production: pre-create this role yourself with a strong password before
-- running migrations — the CREATE below is skipped when the role exists.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'tracksphere_app') THEN
        CREATE ROLE tracksphere_app LOGIN PASSWORD 'tracksphere_app';
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO tracksphere_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO tracksphere_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO tracksphere_app;

-- Cover tables/views created by future migrations (executed by the owner).
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO tracksphere_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO tracksphere_app;