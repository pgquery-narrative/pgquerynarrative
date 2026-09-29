-- Allow API_REQUEST event type for HTTP request audit trail.
ALTER TABLE app.audit_logs DROP CONSTRAINT IF EXISTS audit_logs_event_type_check;
ALTER TABLE app.audit_logs ADD CONSTRAINT audit_logs_event_type_check CHECK (event_type IN (
    'RUN_QUERY', 'GENERATE_REPORT', 'EXPORT_REPORT',
    'SAVE_QUERY', 'DELETE_QUERY', 'UPDATE_QUERY',
    'AUTH_FAILURE', 'AUTH_SUCCESS', 'RATE_LIMIT_EXCEEDED',
    'INVALID_SQL_ATTEMPT', 'UNAUTHORIZED_ACCESS', 'API_REQUEST'
));
