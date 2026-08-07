-- Persist the short natural-language justification returned by custom_json
-- audit models, so admins can see WHY a request was flagged rather than only
-- which categories matched. Empty for the qwen3guard backend, whose response
-- contract has no free-text field.
ALTER TABLE prompt_audit_events
    ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';
