-- 0003_maintenance.sql — alert persistence helpers, the durable Telegram
-- notification outbox, maintenance run bookkeeping and the indexes used by the
-- bounded retention cleanup.
--
-- Alert state itself lives in `events` (one open row per "<kind>:<subject>"
-- dedupe key, plus a recovery row); see internal/panel/store/maintenance.go.
-- The outbox intentionally has no foreign key to events: a pending or failed
-- notification must never disappear because old events were cleaned up.

CREATE TABLE IF NOT EXISTS notification_outbox (
    id              text PRIMARY KEY,
    event_id        text NOT NULL UNIQUE,
    channel         text NOT NULL DEFAULT 'telegram',
    kind            text NOT NULL DEFAULT '',
    subject_type    text NOT NULL DEFAULT '',
    subject_id      text NOT NULL DEFAULT '',
    text            text NOT NULL,
    state           text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sent', 'failed')),
    attempts        integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text NOT NULL DEFAULT '',
    sent_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notification_outbox_due_idx ON notification_outbox (state, next_attempt_at);
CREATE INDEX IF NOT EXISTS notification_outbox_event_idx ON notification_outbox (event_id);

CREATE TABLE IF NOT EXISTS maintenance_state (
    id                     integer PRIMARY KEY CHECK (id = 1),
    runs                   bigint NOT NULL DEFAULT 0,
    alerts_raised          bigint NOT NULL DEFAULT 0,
    alerts_adopted         bigint NOT NULL DEFAULT 0,
    alerts_recovered       bigint NOT NULL DEFAULT 0,
    notifications_enqueued bigint NOT NULL DEFAULT 0,
    notifications_sent     bigint NOT NULL DEFAULT 0,
    notifications_failed   bigint NOT NULL DEFAULT 0,
    rows_cleaned           bigint NOT NULL DEFAULT 0,
    last_run_at            timestamptz,
    last_error             text NOT NULL DEFAULT '',
    updated_at             timestamptz NOT NULL DEFAULT now()
);
INSERT INTO maintenance_state (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

-- Indexes for the bounded cleanup statements.
CREATE INDEX IF NOT EXISTS events_state_created_idx ON events (state, created_at);
CREATE INDEX IF NOT EXISTS previews_expires_idx ON previews (expires_at);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions (expires_at);
CREATE INDEX IF NOT EXISTS invitations_expires_idx ON invitations (expires_at);
CREATE INDEX IF NOT EXISTS registration_codes_expires_idx ON registration_codes (expires_at);
CREATE INDEX IF NOT EXISTS batches_state_completed_idx ON batches (state, completed_at);
