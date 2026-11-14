-- +goose Up
-- Approvals (ship.Approval): a question an agent asks people before doing
-- something consequential, with 2..6 options. The card goes out as an
-- outbound ship (ship_id); the answer comes from the web, or from the
-- channel as the inbound ship that matched an option (answered_ship_id).
-- At most one pending approval per conversation and agent. An approval
-- that ended (answered from the web, cancelled, expired) wakes its agent
-- with a system message; woken_at says that was done (answers from the
-- channel are the agent's delivery already), so a failed wake is retried.
CREATE TABLE ship_approvals (
    organization_id  uuid NOT NULL,
    id               uuid NOT NULL,
    conversation_id  uuid NOT NULL,
    agent_id         uuid NOT NULL,
    delivery_id      uuid,
    ship_id          uuid NOT NULL,
    text             text NOT NULL,
    options          jsonb NOT NULL,
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'answered', 'expired', 'cancelled')),
    answer           text,
    answered_by      text NOT NULL DEFAULT '',
    answered_ship_id uuid,
    expires_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    answered_at      timestamptz,
    ended_at         timestamptz,
    woken_at         timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id) REFERENCES conversations (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents (organization_id, id),
    FOREIGN KEY (organization_id, ship_id) REFERENCES ships (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, answered_ship_id) REFERENCES ships (organization_id, id),
    CHECK ((status = 'answered') = (answer IS NOT NULL)),
    CHECK ((status = 'pending') = (ended_at IS NULL))
);
CREATE INDEX ship_approvals_conversation ON ship_approvals (organization_id, conversation_id, status);
CREATE INDEX ship_approvals_created ON ship_approvals (organization_id, created_at DESC);
CREATE INDEX ship_approvals_unwoken ON ship_approvals (ended_at) WHERE woken_at IS NULL AND status <> 'pending';
CREATE INDEX ship_approvals_expiry ON ship_approvals (expires_at) WHERE status = 'pending';
CREATE UNIQUE INDEX ship_approvals_one_pending ON ship_approvals (organization_id, conversation_id, agent_id) WHERE status = 'pending';

-- +goose Down
DROP TABLE ship_approvals;
