-- Enforce read-only at session level for the readonly role (superuser only).
-- When migrations run as pgquerynarrative_app (Docker entrypoint), skip without error.
DO $$
BEGIN
  IF COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false) THEN
    ALTER ROLE pgquerynarrative_readonly SET default_transaction_read_only = on;
  END IF;
END
$$;
