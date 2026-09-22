-- PgQueryNarrative "pqn" extension: role setup for an installer who is NOT a superuser.
-- If a superuser creates the extension (CREATE EXTENSION pqn), the roles are made for you and this
-- script is not needed. Otherwise run it once per cluster as a DBA (a superuser is the tested path),
-- then run CREATE EXTENSION pqn as the installer.
--
--   psql -d yourdb -v installer=pqn_installer -f pqn-roles.sql
--
-- Review it first. It creates only NOLOGIN roles. Nobody connects as them: they own objects
-- and are the identity that SECURITY DEFINER functions run as.
--
--   pqn_owner    owns the curated views in schema pqn. Holds SELECT on the columns you expose.
--   pqn_reader   runs analyst SQL. Holds SELECT on schema pqn views only.
--   pqn_stats    reads pg_stat_statements (member of pg_monitor). Holds nothing else.
--   pqn_ledger   owns the ledger (schema pqn_ledger). Holds nothing outside it.
--   pqn_viewer, pqn_analyst, pqn_admin   groups people join. pqn_admin includes pqn_analyst,
--                which includes pqn_viewer.
--
-- The installer must be able to hand objects to the four owner roles, so it is made a member
-- of them, and it needs CREATE on the database to create schemas. It stays an ordinary role.
\set ON_ERROR_STOP on
\if :{?installer}
\else
  \set installer :USER
\endif

SELECT format('CREATE ROLE %I NOLOGIN', r)
FROM unnest(ARRAY['pqn_owner', 'pqn_reader', 'pqn_stats', 'pqn_ledger',
                  'pqn_viewer', 'pqn_analyst', 'pqn_admin']) AS r
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r)
\gexec

GRANT pg_monitor TO pqn_stats;
GRANT pqn_viewer TO pqn_analyst;
GRANT pqn_analyst TO pqn_admin;

SELECT format('GRANT %I TO %I', r, :'installer')
FROM unnest(ARRAY['pqn_owner', 'pqn_reader', 'pqn_stats', 'pqn_ledger']) AS r
\gexec

SELECT format('GRANT CREATE ON DATABASE %I TO %I', current_database(), :'installer')
\gexec
