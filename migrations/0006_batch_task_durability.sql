-- Permanent submission receipts: deliberately NO foreign key to retained history.
-- result is the submission response, not a mutable dispatch-status cache.
CREATE TABLE batch_idempotency (
    idempotency_key text PRIMARY KEY,
    request_fingerprint text NOT NULL,
    fingerprint_version integer NOT NULL CHECK (fingerprint_version IN (1,2)),
    batch_id text NOT NULL UNIQUE,
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE tasks ADD COLUMN revision bigint CHECK (revision > 0);

-- Unproven history stays visible and cannot be dispatched by RetryTask.
CREATE TABLE batch_durability_diagnostics (
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    reason text NOT NULL,
    PRIMARY KEY (subject_type, subject_id)
);

-- The old writer allocated several item IDs but retained only the last task
-- per agent. Repair only when precisely one compatible task actually exists.
WITH candidates AS (
    SELECT i.id, min(t.id) AS task_id, count(*) AS n
    FROM batch_items i JOIN tasks t ON t.batch_id=i.batch_id
      AND t.agent_id=i.agent_id AND t.kind='authorization.sync'
      AND (t.node_id='' OR t.node_id=i.node_id)
      AND (t.member_id='' OR t.member_id=i.member_id)
    WHERE i.agent_id<>''
    GROUP BY i.id
)
UPDATE batch_items i SET task_id=c.task_id
FROM candidates c WHERE i.id=c.id AND c.n=1 AND i.task_id<>c.task_id;

INSERT INTO batch_durability_diagnostics
SELECT 'batch_item', i.id, 'task mapping is missing, incompatible or ambiguous; manual evidence required'
FROM batch_items i WHERE i.agent_id<>'' AND (
    (SELECT count(*) FROM tasks t WHERE t.batch_id=i.batch_id AND t.agent_id=i.agent_id
       AND t.kind='authorization.sync' AND (t.node_id='' OR t.node_id=i.node_id)
       AND (t.member_id='' OR t.member_id=i.member_id)) <> 1
    OR NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id=i.task_id AND t.batch_id=i.batch_id
       AND t.agent_id=i.agent_id AND t.kind='authorization.sync'));

-- No desired_revision/ledger/authorization reconstruction: only unanimous
-- positive item revisions from an unambiguous task mapping prove publication.
WITH proven AS (
    SELECT t.id, min(i.revision) AS revision
    FROM tasks t JOIN batch_items i ON i.task_id=t.id
    WHERE t.kind='authorization.sync' AND t.batch_id<>''
    GROUP BY t.id
    HAVING min(i.revision)>0 AND min(i.revision)=max(i.revision)
       AND bool_and(i.batch_id=t.batch_id AND i.agent_id=t.agent_id)
       AND NOT EXISTS (SELECT 1 FROM batch_durability_diagnostics d
         JOIN batch_items x ON x.id=d.subject_id
         WHERE d.subject_type='batch_item' AND x.batch_id=t.batch_id AND x.agent_id=t.agent_id)
)
UPDATE tasks t SET revision=p.revision FROM proven p WHERE t.id=p.id;

INSERT INTO batch_durability_diagnostics
SELECT 'task', id, 'published revision is not provable; retry blocked pending manual evidence'
FROM tasks WHERE revision IS NULL;

-- Old fingerprints have their original (preview_id, confirm) meaning. Preserve
-- that known fact, never manufacture the caller's omitted optional fields.
INSERT INTO batch_idempotency (idempotency_key, request_fingerprint, fingerprint_version, batch_id, result, created_at)
SELECT b.idempotency_key, b.request_fingerprint, 1, b.id,
 jsonb_build_object('batch', jsonb_build_object(
   'id',b.id,'preview_id',b.preview_id,'operation',b.operation,'state',b.state,
   'idempotency_key',b.idempotency_key,'created_by',b.created_by,'created_at',b.created_at,
   'completed_at',b.completed_at,'results',items.rows,'summary',b.summary),
   'results',items.rows,'summary',b.summary,'idempotent_replay',false), b.created_at
FROM batches b CROSS JOIN LATERAL (
 SELECT COALESCE(jsonb_agg(jsonb_build_object('id',i.id,'batch_id',i.batch_id,
   'member_id',i.member_id,'node_id',i.node_id,'agent_id',i.agent_id,'state',i.state,
   'task_id',i.task_id,'error',i.error) ORDER BY i.created_at,i.id), '[]'::jsonb) AS rows
 FROM batch_items i WHERE i.batch_id=b.id
) items;

-- Duplicate proven publications are evidence conflicts: fail this file's
-- transaction rather than choosing/deleting a historical task.
CREATE UNIQUE INDEX tasks_batch_agent_revision_key ON tasks (batch_id, agent_id, revision)
    WHERE batch_id<>'' AND revision IS NOT NULL;
CREATE INDEX tasks_agent_revision_state_idx ON tasks (agent_id, revision, state);
CREATE INDEX tasks_batch_agent_idx ON tasks (batch_id, agent_id);
CREATE INDEX batch_items_task_idx ON batch_items (task_id);
-- Fail-safe: cleanup cannot remove history without its durable receipt.
CREATE INDEX batch_items_agent_revision_state_idx ON batch_items (agent_id, revision, state);
