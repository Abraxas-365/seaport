-- +goose Up
-- Environment secrets (plan env-and-secrets.md): a model key or a repo
-- credential is infrastructure the environment's slots share, so the
-- environment holds it, sealed like an agent's secrets (seal, purpose
-- environment-secret/<org>/<env>/<name>). env: also an environment
-- variable of every slot's harness. Specs that name an agent's secret keep
-- working: a slot opens the environment's secret first, else the agent's.
CREATE TABLE environment_secrets (
    organization_id uuid NOT NULL,
    environment_id  uuid NOT NULL,
    name            text NOT NULL,
    sealed          bytea NOT NULL,
    env             boolean NOT NULL DEFAULT false,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, environment_id, name),
    FOREIGN KEY (organization_id, environment_id) REFERENCES environments (organization_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE environment_secrets;
