DO $$
BEGIN
  IF COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false) THEN
    ALTER ROLE pgquerynarrative_readonly RESET default_transaction_read_only;
  END IF;
END
$$;
