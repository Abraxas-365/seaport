-- +goose Up
-- Customs rules per dock (see conversation/customs.go).
CREATE TABLE customs_policies (
    dock_id        uuid PRIMARY KEY REFERENCES docks (id) ON DELETE CASCADE,
    default_access text NOT NULL CHECK (default_access IN ('ask', 'allow', 'deny')),
    group_mode     text NOT NULL CHECK (group_mode IN ('mentions', 'all')),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE customs_rules (
    id         uuid PRIMARY KEY,
    dock_id    uuid NOT NULL REFERENCES docks (id) ON DELETE CASCADE,
    effect     text NOT NULL CHECK (effect IN ('allow', 'deny')),
    pattern    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (dock_id, effect, pattern)
);

CREATE TABLE conversation_handles (
    conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    handle          text NOT NULL,
    PRIMARY KEY (conversation_id, handle)
);

ALTER TABLE conversations
    ADD COLUMN access text NOT NULL DEFAULT 'auto' CHECK (access IN ('auto', 'allow', 'deny')),
    ADD COLUMN group_mode text NOT NULL DEFAULT '' CHECK (group_mode IN ('', 'mentions', 'all')),
    ADD COLUMN status text NOT NULL DEFAULT 'pending' CHECK (status IN ('allowed', 'pending', 'denied'));

-- Explicit allows become owner overrides; self chats stay allowed by kind.
UPDATE conversations SET access = 'allow' WHERE allowed AND kind <> 'self';
UPDATE conversations SET status = CASE WHEN allowed THEN 'allowed' ELSE 'pending' END;
INSERT INTO conversation_handles (conversation_id, handle)
    SELECT id, lower(external_id) FROM conversations;

ALTER TABLE conversations DROP COLUMN allowed;

-- +goose Down
ALTER TABLE conversations ADD COLUMN allowed boolean NOT NULL DEFAULT false;
UPDATE conversations SET allowed = (status = 'allowed');
ALTER TABLE conversations DROP COLUMN status, DROP COLUMN group_mode, DROP COLUMN access;
DROP TABLE conversation_handles;
DROP TABLE customs_rules;
DROP TABLE customs_policies;
