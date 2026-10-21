-- 0001_init.sql — proxypanel core schema.
-- All identifiers are server generated opaque strings; credentials and
-- transport secrets are stored encrypted (bytea) with PANEL_MASTER_KEY.

CREATE TABLE servers (
    id           text PRIMARY KEY,
    name         text NOT NULL,
    address      text NOT NULL DEFAULT '',
    region       text NOT NULL DEFAULT '',
    notes        text NOT NULL DEFAULT '',
    expires_at   timestamptz,
    cost_minor   bigint,
    currency     text NOT NULL DEFAULT '',
    archived     boolean NOT NULL DEFAULT false,
    version      bigint NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX servers_name_key ON servers (lower(name));

CREATE TABLE templates (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    format      text NOT NULL CHECK (format IN ('xray', 'mihomo', 'singbox', 'v2rayn')),
    content     jsonb NOT NULL DEFAULT '{}'::jsonb,
    description text NOT NULL DEFAULT '',
    version     bigint NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX templates_name_key ON templates (lower(name));

CREATE TABLE node_groups (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    version     bigint NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX node_groups_name_key ON node_groups (lower(name));

CREATE TABLE nodes (
    id                text PRIMARY KEY,
    server_id         text NOT NULL REFERENCES servers (id),
    name              text NOT NULL,
    enabled           boolean NOT NULL DEFAULT true,
    host              text NOT NULL,
    port              integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    listen            text NOT NULL DEFAULT '0.0.0.0',
    protocol          text NOT NULL,
    flow              text NOT NULL DEFAULT '',
    cipher            text NOT NULL DEFAULT '',
    server_key_enc    bytea,
    stream_settings   jsonb NOT NULL DEFAULT '{}'::jsonb,
    protocol_settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    client_settings   jsonb NOT NULL DEFAULT '{}'::jsonb,
    sort_order        integer NOT NULL DEFAULT 0,
    archived          boolean NOT NULL DEFAULT false,
    version           bigint NOT NULL DEFAULT 1,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX nodes_name_key ON nodes (lower(name));

CREATE TABLE node_group_members (
    group_id text NOT NULL REFERENCES node_groups (id) ON DELETE CASCADE,
    node_id  text NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    position integer NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, node_id)
);
CREATE INDEX node_group_members_node_idx ON node_group_members (node_id);

CREATE TABLE subscriptions (
    id                 text PRIMARY KEY,
    name               text NOT NULL,
    description        text NOT NULL DEFAULT '',
    quota_bytes        bigint CHECK (quota_bytes IS NULL OR quota_bytes >= 0),
    timezone           text NOT NULL DEFAULT 'Asia/Shanghai',
    anchor_day         integer NOT NULL DEFAULT 1 CHECK (anchor_day BETWEEN 1 AND 31),
    default_valid_days integer CHECK (default_valid_days IS NULL OR default_valid_days > 0),
    allow_new_bindings boolean NOT NULL DEFAULT true,
    access_enabled     boolean NOT NULL DEFAULT true,
    archived           boolean NOT NULL DEFAULT false,
    template_id        text REFERENCES templates (id) ON DELETE SET NULL,
    pending_period_rule jsonb,
    version            bigint NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX subscriptions_name_key ON subscriptions (lower(name));

CREATE TABLE subscription_node_groups (
    subscription_id text NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    node_group_id   text NOT NULL REFERENCES node_groups (id) ON DELETE CASCADE,
    position        integer NOT NULL DEFAULT 0,
    PRIMARY KEY (subscription_id, node_group_id)
);

CREATE TABLE members (
    id                     text PRIMARY KEY,
    username               text NOT NULL,
    password_hash          text NOT NULL,
    role                   text NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
    active                 boolean NOT NULL DEFAULT true,
    proxy_enabled          boolean NOT NULL DEFAULT true,
    archived               boolean NOT NULL DEFAULT false,
    subscription_id        text REFERENCES subscriptions (id),
    expires_at             timestamptz,
    quota_override_bytes   bigint CHECK (quota_override_bytes IS NULL OR quota_override_bytes >= 0),
    quota_override_enabled boolean NOT NULL DEFAULT false,
    period_start           timestamptz,
    period_end             timestamptz,
    period_timezone        text NOT NULL DEFAULT '',
    period_anchor_day      integer NOT NULL DEFAULT 0,
    pending_period_rule    jsonb,
    over_quota             boolean NOT NULL DEFAULT false,
    bound_at               timestamptz,
    last_login_at          timestamptz,
    version                bigint NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX members_username_key ON members (lower(username));
CREATE INDEX members_subscription_idx ON members (subscription_id);

CREATE TABLE member_bindings (
    id              text PRIMARY KEY,
    member_id       text NOT NULL REFERENCES members (id),
    subscription_id text NOT NULL REFERENCES subscriptions (id),
    bound_at        timestamptz NOT NULL DEFAULT now(),
    ended_at        timestamptz,
    expires_at      timestamptz,
    reason          text NOT NULL DEFAULT '',
    created_by      text NOT NULL DEFAULT ''
);
CREATE INDEX member_bindings_member_idx ON member_bindings (member_id, bound_at DESC);

CREATE TABLE agents (
    id                  text PRIMARY KEY,
    server_id           text NOT NULL REFERENCES servers (id),
    name                text NOT NULL DEFAULT '',
    version             text NOT NULL DEFAULT '',
    xray_version        text NOT NULL DEFAULT '',
    token_hash          text NOT NULL UNIQUE,
    token_prefix        text NOT NULL DEFAULT '',
    control_generation  bigint NOT NULL DEFAULT 1,
    desired_revision    bigint NOT NULL DEFAULT 0,
    applied_revision    bigint NOT NULL DEFAULT 0,
    applied_at          timestamptz,
    last_heartbeat_at   timestamptz,
    uptime_seconds      bigint NOT NULL DEFAULT 0,
    queue_depth         bigint NOT NULL DEFAULT 0,
    metrics             jsonb NOT NULL DEFAULT '{}'::jsonb,
    instances           jsonb NOT NULL DEFAULT '[]'::jsonb,
    archived            boolean NOT NULL DEFAULT false,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agents_server_idx ON agents (server_id);

CREATE TABLE core_instances (
    id              text PRIMARY KEY,
    server_id       text NOT NULL REFERENCES servers (id),
    agent_id        text REFERENCES agents (id) ON DELETE SET NULL,
    node_id         text NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    generation      bigint NOT NULL DEFAULT 0,
    config_revision bigint NOT NULL DEFAULT 0,
    state           text NOT NULL DEFAULT 'stopped',
    last_started_at timestamptz,
    applied_at      timestamptz,
    last_error      text NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX core_instances_node_key ON core_instances (node_id);

CREATE TABLE credentials (
    id             text PRIMARY KEY,
    member_id      text NOT NULL REFERENCES members (id),
    node_id        text NOT NULL REFERENCES nodes (id),
    kind           text NOT NULL DEFAULT 'uuid',
    secret_enc     bytea NOT NULL,
    state          text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'revoked')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    revoked_at     timestamptz,
    rotated_reason text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX credentials_active_key ON credentials (member_id, node_id) WHERE state = 'active';
CREATE INDEX credentials_node_idx ON credentials (node_id);

CREATE TABLE subscription_tokens (
    id           text PRIMARY KEY,
    member_id    text NOT NULL REFERENCES members (id),
    token_hash   text NOT NULL UNIQUE,
    token_enc    bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    rotated_at   timestamptz,
    revoked_at   timestamptz
);
CREATE UNIQUE INDEX subscription_tokens_active_key ON subscription_tokens (member_id) WHERE revoked_at IS NULL;

CREATE TABLE sessions (
    id           text PRIMARY KEY,
    member_id    text NOT NULL REFERENCES members (id),
    token_hash   text NOT NULL UNIQUE,
    csrf_token   text NOT NULL,
    ip           text NOT NULL DEFAULT '',
    user_agent   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz
);
CREATE INDEX sessions_member_idx ON sessions (member_id);

CREATE TABLE invitations (
    id                 text PRIMARY KEY,
    code_hash          text NOT NULL UNIQUE,
    subscription_id    text REFERENCES subscriptions (id),
    expires_at         timestamptz NOT NULL,
    max_uses           integer NOT NULL DEFAULT 1 CHECK (max_uses > 0),
    used_count         integer NOT NULL DEFAULT 0,
    used_at            timestamptz,
    used_by_member_id  text REFERENCES members (id),
    revoked_at         timestamptz,
    created_by         text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE registration_codes (
    id               text PRIMARY KEY,
    server_id        text NOT NULL REFERENCES servers (id),
    code_hash        text NOT NULL UNIQUE,
    expires_at       timestamptz NOT NULL,
    used_at          timestamptz,
    used_by_agent_id text REFERENCES agents (id),
    created_by       text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE quota_adjustments (
    id           text PRIMARY KEY,
    member_id    text NOT NULL REFERENCES members (id),
    bytes        bigint NOT NULL,
    reason       text NOT NULL,
    period_start timestamptz,
    created_by   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX quota_adjustments_member_idx ON quota_adjustments (member_id, created_at DESC);

CREATE TABLE usage_batches (
    agent_id       text NOT NULL,
    batch_id       text NOT NULL,
    payload_sha256 text NOT NULL,
    status         text NOT NULL DEFAULT 'accepted',
    sample_count   integer NOT NULL DEFAULT 0,
    started_at     timestamptz,
    ended_at       timestamptz,
    core_generation text NOT NULL DEFAULT '',
    clock_skew     boolean NOT NULL DEFAULT false,
    received_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, batch_id)
);

CREATE TABLE usage_entries (
    id                bigserial PRIMARY KEY,
    agent_id          text NOT NULL,
    batch_id          text NOT NULL,
    member_id         text NOT NULL,
    node_id           text NOT NULL,
    credential_id     text NOT NULL DEFAULT '',
    period_start      timestamptz NOT NULL,
    period_end        timestamptz NOT NULL,
    up_bytes          bigint NOT NULL CHECK (up_bytes >= 0),
    down_bytes        bigint NOT NULL CHECK (down_bytes >= 0),
    sample_started_at timestamptz NOT NULL,
    sample_ended_at   timestamptz NOT NULL,
    received_at       timestamptz NOT NULL DEFAULT now(),
    estimated         boolean NOT NULL DEFAULT false,
    boundary_split    boolean NOT NULL DEFAULT false,
    correction        boolean NOT NULL DEFAULT false,
    core_generation   text NOT NULL DEFAULT '',
    split_part        integer NOT NULL DEFAULT 0,
    UNIQUE (agent_id, batch_id, member_id, node_id, sample_started_at, sample_ended_at, period_start, split_part)
);
CREATE INDEX usage_entries_member_period_idx ON usage_entries (member_id, period_start);
CREATE INDEX usage_entries_node_period_idx ON usage_entries (node_id, period_start);

CREATE TABLE previews (
    id                text PRIMARY KEY,
    operation         text NOT NULL,
    payload           jsonb NOT NULL,
    plan              jsonb NOT NULL,
    rules_version     bigint NOT NULL,
    member_set_digest text NOT NULL,
    created_by        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    consumed_at       timestamptz,
    consumed_by_batch text NOT NULL DEFAULT ''
);

CREATE TABLE batches (
    id                  text PRIMARY KEY,
    preview_id          text NOT NULL,
    operation           text NOT NULL,
    state               text NOT NULL DEFAULT 'applied',
    idempotency_key     text NOT NULL UNIQUE,
    request_fingerprint text NOT NULL,
    batch_ids           jsonb NOT NULL DEFAULT '[]'::jsonb,
    summary             jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by          text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz
);

CREATE TABLE batch_items (
    id         text PRIMARY KEY,
    batch_id   text NOT NULL REFERENCES batches (id) ON DELETE CASCADE,
    member_id  text NOT NULL DEFAULT '',
    node_id    text NOT NULL DEFAULT '',
    agent_id   text NOT NULL DEFAULT '',
    state      text NOT NULL DEFAULT 'pending',
    task_id    text NOT NULL DEFAULT '',
    error      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX batch_items_batch_idx ON batch_items (batch_id);

CREATE TABLE tasks (
    id              text PRIMARY KEY,
    kind            text NOT NULL,
    agent_id        text NOT NULL DEFAULT '',
    node_id         text NOT NULL DEFAULT '',
    member_id       text NOT NULL DEFAULT '',
    batch_id        text NOT NULL DEFAULT '',
    state           text NOT NULL DEFAULT 'pending',
    attempts        integer NOT NULL DEFAULT 0,
    max_attempts    integer NOT NULL DEFAULT 5,
    last_error      text NOT NULL DEFAULT '',
    available_at    timestamptz NOT NULL DEFAULT now(),
    locked_at       timestamptz,
    idempotency_key text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tasks_idempotency_key ON tasks (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX tasks_state_idx ON tasks (state, available_at);

CREATE TABLE events (
    id           text PRIMARY KEY,
    kind         text NOT NULL,
    severity     text NOT NULL DEFAULT 'info',
    subject_type text NOT NULL DEFAULT '',
    subject_id   text NOT NULL DEFAULT '',
    message      text NOT NULL DEFAULT '',
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    state        text NOT NULL DEFAULT 'open',
    dedupe_key   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    acked_at     timestamptz,
    acked_by     text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX events_open_dedupe ON events (dedupe_key) WHERE dedupe_key IS NOT NULL AND state = 'open';
CREATE INDEX events_created_idx ON events (created_at DESC);

CREATE TABLE audit_logs (
    id             text PRIMARY KEY,
    actor_id       text NOT NULL DEFAULT '',
    actor_username text NOT NULL DEFAULT '',
    action         text NOT NULL,
    target_type    text NOT NULL DEFAULT '',
    target_id      text NOT NULL DEFAULT '',
    detail         jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip             text NOT NULL DEFAULT '',
    request_id     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC);

CREATE TABLE settings (
    id                 integer PRIMARY KEY CHECK (id = 1),
    timezone           text NOT NULL DEFAULT 'Asia/Shanghai',
    retention_days     integer NOT NULL DEFAULT 90 CHECK (retention_days > 0),
    telegram_enabled   boolean NOT NULL DEFAULT false,
    telegram_chat_id   text NOT NULL DEFAULT '',
    telegram_token_enc bytea,
    telegram_token_set boolean NOT NULL DEFAULT false,
    template_defaults  jsonb NOT NULL DEFAULT '{}'::jsonb,
    version            bigint NOT NULL DEFAULT 1,
    updated_at         timestamptz NOT NULL DEFAULT now()
);
INSERT INTO settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE control_state (
    id                 integer PRIMARY KEY CHECK (id = 1),
    control_generation bigint NOT NULL DEFAULT 1,
    revision_counter   bigint NOT NULL DEFAULT 0,
    recovery_mode      boolean NOT NULL DEFAULT false,
    recovery_entered_at timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT now()
);
INSERT INTO control_state (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE idempotency_records (
    key         text PRIMARY KEY,
    scope       text NOT NULL DEFAULT '',
    fingerprint text NOT NULL,
    result      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agent_receipts (
    agent_id   text NOT NULL,
    revision   bigint NOT NULL,
    state      text NOT NULL DEFAULT '',
    applied_at timestamptz,
    payload    jsonb NOT NULL DEFAULT '{}'::jsonb,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, revision)
);
