-- 0002_batch_preview.sql — batch preview/submit bookkeeping.
ALTER TABLE previews ADD COLUMN IF NOT EXISTS conclusion jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE previews ADD COLUMN IF NOT EXISTS affected_nodes integer NOT NULL DEFAULT 0;
ALTER TABLE previews ADD COLUMN IF NOT EXISTS affected_agents integer NOT NULL DEFAULT 0;
ALTER TABLE previews ADD COLUMN IF NOT EXISTS member_count integer NOT NULL DEFAULT 0;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS member_count integer NOT NULL DEFAULT 0;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS affected_nodes integer NOT NULL DEFAULT 0;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS rules_version bigint NOT NULL DEFAULT 0;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS member_set_digest text NOT NULL DEFAULT '';
ALTER TABLE batches ADD COLUMN IF NOT EXISTS request jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE batch_items ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 0;
ALTER TABLE batch_items ADD COLUMN IF NOT EXISTS reason text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS batch_items_agent_state_idx ON batch_items (agent_id, state);
CREATE INDEX IF NOT EXISTS batches_preview_idx ON batches (preview_id);
