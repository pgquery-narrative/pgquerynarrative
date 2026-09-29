-- Set readonly role search_path to demo (defense-in-depth with connection AfterConnect).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    ALTER ROLE pgquerynarrative_readonly SET search_path = demo;
  END IF;
END $$;
