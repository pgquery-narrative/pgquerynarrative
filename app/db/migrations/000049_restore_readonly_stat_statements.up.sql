-- Restore pg_stat_statements observability for the read-only analytical role.
-- Migration 000023 revoked pg_read_all_stats from pgquerynarrative_readonly while
-- the stats API still queries through the read-only pool, which broke GET /api/v1/queries/stats.
--
-- This GRANT requires a superuser (or ADMIN OPTION on pg_read_all_stats). When the
-- app role runs migrations at container start, skip quietly — use `make migrate-docker`
-- (postgres superuser) for fresh installs. The Go query also qualifies
-- public.pg_stat_statements so search_path=demo does not hide the view.

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    BEGIN
      GRANT pg_read_all_stats TO pgquerynarrative_readonly;
    EXCEPTION
      WHEN insufficient_privilege THEN
        RAISE NOTICE 'skip GRANT pg_read_all_stats to readonly (need superuser migrate)';
      WHEN OTHERS THEN
        IF SQLERRM LIKE '%permission denied%pg_read_all_stats%' OR SQLERRM LIKE '%ADMIN option%' THEN
          RAISE NOTICE 'skip GRANT pg_read_all_stats to readonly: %', SQLERRM;
        ELSE
          RAISE;
        END IF;
    END;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
    BEGIN
      GRANT pg_read_all_stats TO pgquerynarrative_app;
    EXCEPTION
      WHEN insufficient_privilege THEN
        RAISE NOTICE 'skip GRANT pg_read_all_stats to app (need superuser migrate)';
      WHEN OTHERS THEN
        IF SQLERRM LIKE '%permission denied%pg_read_all_stats%' OR SQLERRM LIKE '%ADMIN option%' THEN
          RAISE NOTICE 'skip GRANT pg_read_all_stats to app: %', SQLERRM;
        ELSE
          RAISE;
        END IF;
    END;
  END IF;
END $$;
