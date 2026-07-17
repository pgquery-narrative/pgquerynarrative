-- Harden the analytical role as the database-side enforcement boundary.
-- Some ALTER ROLE options require superuser/CREATEROLE; when migrations run as
-- the app user, keep this migration non-fatal and rely on the documented
-- production setup/diagnostic path to verify the role.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    IF COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false) THEN
      ALTER ROLE pgquerynarrative_readonly
        NOSUPERUSER
        NOCREATEDB
        NOCREATEROLE
        NOINHERIT
        NOREPLICATION
        NOBYPASSRLS;

      ALTER ROLE pgquerynarrative_readonly SET default_transaction_read_only = on;
      ALTER ROLE pgquerynarrative_readonly SET statement_timeout = '30s';
      ALTER ROLE pgquerynarrative_readonly SET lock_timeout = '2s';
      ALTER ROLE pgquerynarrative_readonly SET idle_in_transaction_session_timeout = '10s';
    END IF;
  END IF;
END $$;
