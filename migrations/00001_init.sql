-- +goose Up
CREATE TABLE docks (
    id         uuid PRIMARY KEY,
    channel    text NOT NULL CHECK (channel IN ('whatsapp', 'api')),
    name       text NOT NULL,
    address    text NOT NULL DEFAULT '',
    status     text NOT NULL CHECK (status IN ('linking', 'linked', 'logged_out', 'failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE conversations (
    id          uuid PRIMARY KEY,
    dock_id     uuid NOT NULL REFERENCES docks (id) ON DELETE CASCADE,
    channel     text NOT NULL,
    external_id text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('direct', 'group', 'self')),
    title       text NOT NULL DEFAULT '',
    allowed     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (dock_id, external_id)
);

CREATE TABLE ships (
    id              uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    direction       text NOT NULL CHECK (direction IN ('in', 'out')),
    external_id     text NOT NULL,
    sender_id       text NOT NULL DEFAULT '',
    sender_name     text NOT NULL DEFAULT '',
    text            text NOT NULL,
    status          text NOT NULL CHECK (status IN ('held', 'queued', 'claimed', 'handled', 'pending', 'sent', 'failed')),
    delivery_id     uuid,
    claimed_until   timestamptz,
    error           text NOT NULL DEFAULT '',
    sent_at         timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, external_id),
    CHECK ((status = 'claimed') = (claimed_until IS NOT NULL))
);

CREATE INDEX ships_queue ON ships (created_at, id) WHERE status = 'queued';
CREATE INDEX ships_claimed ON ships (conversation_id) WHERE status = 'claimed';
CREATE INDEX ships_delivery ON ships (delivery_id) WHERE delivery_id IS NOT NULL;
CREATE INDEX ships_history ON ships (conversation_id, sent_at DESC, created_at DESC, id DESC);

-- +goose Down
DROP TABLE ships;
DROP TABLE conversations;
DROP TABLE docks;
