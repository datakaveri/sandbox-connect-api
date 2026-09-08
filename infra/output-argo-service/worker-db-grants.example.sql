-- Example only. Run as the database owner after creating a login role through
-- the platform secret manager. Replace the role name before review/apply.
-- The controller does not need access to notebooks, bookings, or profiles.
GRANT SELECT, UPDATE ON TABLE output_jobs TO REPLACE_WITH_OUTPUT_WORKER_ROLE;
GRANT SELECT, UPDATE ON TABLE output_approvals TO REPLACE_WITH_OUTPUT_WORKER_ROLE;
