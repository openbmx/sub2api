-- Record audit failures as events so an operator can see how often the guard
-- timed out or was unreachable, and whether those requests were rejected or
-- passed through. Previously a failure produced only a service log line, so the
-- events view showed nothing at all for a request the gateway had rejected.
ALTER TABLE prompt_audit_events
    ADD COLUMN IF NOT EXISTS error_code VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE prompt_audit_events
    DROP CONSTRAINT IF EXISTS chk_prompt_audit_events_decision;
ALTER TABLE prompt_audit_events
    ADD CONSTRAINT chk_prompt_audit_events_decision
    CHECK (decision IN ('pass', 'flag', 'critical', 'unavailable'));

CREATE INDEX IF NOT EXISTS idx_prompt_audit_events_error_code_created
    ON prompt_audit_events(error_code, created_at DESC, id DESC)
    WHERE error_code <> '';
