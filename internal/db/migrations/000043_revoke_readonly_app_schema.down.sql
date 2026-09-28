-- Down does not re-grant app schema access to readonly (intentionally fail-closed).
-- Public grants remain revoked (see 000023).
SELECT 1;
