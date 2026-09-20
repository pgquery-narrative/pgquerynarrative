-- The audit_logs event_type allowlist stopped at 000008 while the code kept adding event types.
-- Key create and revoke, membership change, connection-authorization change, share create and
-- revoke and raw-SQL view were all rejected by this constraint, so they were either dropped
-- silently (best_effort) or failed the request after the change had been applied (required).
-- This lists every event type the application emits.
ALTER TABLE app.audit_logs DROP CONSTRAINT IF EXISTS audit_logs_event_type_check;
ALTER TABLE app.audit_logs ADD CONSTRAINT audit_logs_event_type_check CHECK (event_type IN (
    'RUN_QUERY', 'GENERATE_REPORT', 'EXPORT_REPORT',
    'SAVE_QUERY', 'DELETE_QUERY', 'UPDATE_QUERY',
    'AUTH_FAILURE', 'AUTH_SUCCESS', 'RATE_LIMIT_EXCEEDED',
    'INVALID_SQL_ATTEMPT', 'UNAUTHORIZED_ACCESS', 'API_REQUEST',
    'VIEW_RAW_SQL', 'CREATE_SHARE', 'REVOKE_SHARE',
    'MANAGED_KEY_CREATE', 'MANAGED_KEY_REVOKE', 'MEMBERSHIP_CHANGE', 'CONNECTION_AUTHZ_CHANGE'
));
