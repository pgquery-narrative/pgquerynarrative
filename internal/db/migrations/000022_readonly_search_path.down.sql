DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    ALTER ROLE pgquerynarrative_readonly RESET search_path;
  END IF;
END $$;
