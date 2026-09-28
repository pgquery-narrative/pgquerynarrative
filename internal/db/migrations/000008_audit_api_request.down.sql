-- Restore the pre-API_REQUEST event-type allowlist.
--
-- Added as NOT VALID on purpose. Any deployment that has served a single HTTP
-- request has API_REQUEST rows in app.audit_logs, and a plain CHECK is verified
-- against existing rows, so re-adding it would fail and make the rollback
-- impossible. The alternative used by later migrations (000037, 000045) is to
-- UPDATE offending rows to an allowed value first — correct for a status column,
-- but not here: rewriting or deleting rows in an audit log falsifies the record
-- it exists to preserve.
--
-- NOT VALID enforces the constraint on every new INSERT and UPDATE while leaving
-- history intact, which is the behaviour a rollback actually wants.
ALTER TABLE app.audit_logs DROP CONSTRAINT IF EXISTS audit_logs_event_type_check;
ALTER TABLE app.audit_logs ADD CONSTRAINT audit_logs_event_type_check CHECK (event_type IN (
    'RUN_QUERY', 'GENERATE_REPORT', 'EXPORT_REPORT',
    'SAVE_QUERY', 'DELETE_QUERY', 'UPDATE_QUERY',
    'AUTH_FAILURE', 'AUTH_SUCCESS', 'RATE_LIMIT_EXCEEDED',
    'INVALID_SQL_ATTEMPT', 'UNAUTHORIZED_ACCESS'
)) NOT VALID;
