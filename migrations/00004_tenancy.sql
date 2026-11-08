-- +goose Up
-- Tenancy: every tenant row belongs to an organization (an IAMKit
-- organization id in iam mode; the fixed default organization in the
-- single-organization modes, see config.DefaultOrganization). Composite
-- foreign keys make a child's organization always equal its parent's.

ALTER TABLE docks ADD COLUMN organization_id uuid NOT NULL
    DEFAULT '00000000-0000-4000-8000-000000000001';
ALTER TABLE docks ALTER COLUMN organization_id DROP DEFAULT;
ALTER TABLE docks ADD CONSTRAINT docks_organization_id_id_key UNIQUE (organization_id, id);
CREATE INDEX docks_org ON docks (organization_id, created_at, id);

ALTER TABLE conversations ADD COLUMN organization_id uuid;
UPDATE conversations c SET organization_id = d.organization_id FROM docks d WHERE d.id = c.dock_id;
ALTER TABLE conversations ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE conversations DROP CONSTRAINT conversations_dock_id_fkey;
ALTER TABLE conversations ADD CONSTRAINT conversations_dock_fkey
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE;
ALTER TABLE conversations ADD CONSTRAINT conversations_organization_id_id_key UNIQUE (organization_id, id);
DROP INDEX conversations_inbox;
CREATE INDEX conversations_inbox ON conversations (organization_id, last_activity_at DESC, id DESC);

ALTER TABLE ships ADD COLUMN organization_id uuid;
UPDATE ships s SET organization_id = c.organization_id FROM conversations c WHERE c.id = s.conversation_id;
ALTER TABLE ships ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE ships DROP CONSTRAINT ships_conversation_id_fkey;
ALTER TABLE ships ADD CONSTRAINT ships_conversation_fkey
    FOREIGN KEY (organization_id, conversation_id) REFERENCES conversations (organization_id, id) ON DELETE CASCADE;
DROP INDEX ships_queue;
CREATE INDEX ships_queue ON ships (organization_id, created_at, id) WHERE status = 'queued';

ALTER TABLE customs_policies ADD COLUMN organization_id uuid;
UPDATE customs_policies p SET organization_id = d.organization_id FROM docks d WHERE d.id = p.dock_id;
ALTER TABLE customs_policies ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE customs_policies DROP CONSTRAINT customs_policies_dock_id_fkey;
ALTER TABLE customs_policies ADD CONSTRAINT customs_policies_dock_fkey
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE;

ALTER TABLE customs_rules ADD COLUMN organization_id uuid;
UPDATE customs_rules r SET organization_id = d.organization_id FROM docks d WHERE d.id = r.dock_id;
ALTER TABLE customs_rules ALTER COLUMN organization_id SET NOT NULL;
ALTER TABLE customs_rules DROP CONSTRAINT customs_rules_dock_id_fkey;
ALTER TABLE customs_rules ADD CONSTRAINT customs_rules_dock_fkey
    FOREIGN KEY (organization_id, dock_id) REFERENCES docks (organization_id, id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE customs_rules DROP CONSTRAINT customs_rules_dock_fkey;
ALTER TABLE customs_rules ADD CONSTRAINT customs_rules_dock_id_fkey
    FOREIGN KEY (dock_id) REFERENCES docks (id) ON DELETE CASCADE;
ALTER TABLE customs_rules DROP COLUMN organization_id;

ALTER TABLE customs_policies DROP CONSTRAINT customs_policies_dock_fkey;
ALTER TABLE customs_policies ADD CONSTRAINT customs_policies_dock_id_fkey
    FOREIGN KEY (dock_id) REFERENCES docks (id) ON DELETE CASCADE;
ALTER TABLE customs_policies DROP COLUMN organization_id;

DROP INDEX ships_queue;
CREATE INDEX ships_queue ON ships (created_at, id) WHERE status = 'queued';
ALTER TABLE ships DROP CONSTRAINT ships_conversation_fkey;
ALTER TABLE ships ADD CONSTRAINT ships_conversation_id_fkey
    FOREIGN KEY (conversation_id) REFERENCES conversations (id) ON DELETE CASCADE;
ALTER TABLE ships DROP COLUMN organization_id;

DROP INDEX conversations_inbox;
CREATE INDEX conversations_inbox ON conversations (last_activity_at DESC, id DESC);
ALTER TABLE conversations DROP CONSTRAINT conversations_organization_id_id_key;
ALTER TABLE conversations DROP CONSTRAINT conversations_dock_fkey;
ALTER TABLE conversations ADD CONSTRAINT conversations_dock_id_fkey
    FOREIGN KEY (dock_id) REFERENCES docks (id) ON DELETE CASCADE;
ALTER TABLE conversations DROP COLUMN organization_id;

DROP INDEX docks_org;
ALTER TABLE docks DROP CONSTRAINT docks_organization_id_id_key;
ALTER TABLE docks DROP COLUMN organization_id;
