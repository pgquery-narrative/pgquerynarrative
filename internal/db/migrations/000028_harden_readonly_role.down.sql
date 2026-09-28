DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    IF COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false) THEN
      ALTER ROLE pgquerynarrative_readonly RESET default_transaction_read_only;
      ALTER ROLE pgquerynarrative_readonly RESET statement_timeout;
      ALTER ROLE pgquerynarrative_readonly RESET lock_timeout;
      ALTER ROLE pgquerynarrative_readonly RESET idle_in_transaction_session_timeout;
      ALTER ROLE pgquerynarrative_readonly INHERIT;
    END IF;
  END IF;
END $$;
