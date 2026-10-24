-- Schema-only candidate. Production adoption requires preflight before any DDL
-- and verified evidence import in the same migration transaction. No backfill.
CREATE TABLE member_periods (
    member_id text NOT NULL REFERENCES members (id),
    period_start timestamptz NOT NULL,
    period_end timestamptz NOT NULL CHECK (period_end > period_start),
    anchor_day integer NOT NULL CHECK (anchor_day BETWEEN 1 AND 31),
    timezone text NOT NULL,
    source text NOT NULL CHECK (source IN ('binding', 'roll_forward', 'proven_current', 'proven_ledger')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (member_id, period_start)
);
CREATE INDEX member_periods_member_end_idx ON member_periods (member_id, period_end);

CREATE TABLE node_server_history (
    id text PRIMARY KEY,
    node_id text NOT NULL REFERENCES nodes (id),
    server_id text NOT NULL REFERENCES servers (id),
    agent_id text NOT NULL REFERENCES agents (id),
    valid_from timestamptz NOT NULL,
    valid_to timestamptz CHECK (valid_to IS NULL OR valid_to >= valid_from),
    released_revision bigint,
    release_confirmed_at timestamptz,
    CHECK (valid_to IS NULL OR (released_revision IS NOT NULL AND release_confirmed_at IS NOT NULL))
);
CREATE INDEX node_server_history_node_end_idx ON node_server_history (node_id, valid_to);
CREATE INDEX node_server_history_agent_end_idx ON node_server_history (agent_id, valid_to);
CREATE INDEX node_server_history_server_start_idx ON node_server_history (server_id, valid_from);
CREATE UNIQUE INDEX node_server_history_release_idx
    ON node_server_history (node_id, agent_id, released_revision)
    WHERE released_revision IS NOT NULL;
CREATE UNIQUE INDEX node_server_history_current_idx
    ON node_server_history (node_id, agent_id)
    WHERE valid_to IS NULL AND released_revision IS NULL;
