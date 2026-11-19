# Back up, restore and upgrade

Know what to save, how to bring a port back, and how to move to a new version without losing anything.

A Seaport port keeps its state in three places. Back up all three, or a restore will be missing pieces.

| What | Where | Why it matters |
| :--- | :--- | :--- |
| **Postgres** | The `postgres` service's database. | Docks, chats, messages, customs, agents (their configs, with the machines of hosted agents) and sealed secrets. |
| **The secret key** | `SEAPORT_SECRET_KEY`, or `/data/secret.key` in the `seaport_data` volume when Seaport generated it. | Opens everything sealed in the database. |
| **The blob store** | `/data/blobs` in `seaport_data` by default, or your `SEAPORT_BLOB_URL` bucket. | Message images and harness homes. Dumps don't hold them. |

## Backups with the compose file

The `backup` service in `deploy/` writes a custom-format `pg_dump` into `deploy/backups` every `BACKUP_EVERY_SECONDS` (a day by default) and deletes dumps older than `BACKUP_KEEP_DAYS` (14). Files are mode `0600`, owned by `BACKUP_UID` (root by default; set it to your user id to copy them without `sudo`).

Dumps cover Postgres only. For the rest:

- **The secret key.** Copy it apart from the dumps. Take it from your `.env` if you set it, or from the volume if Seaport generated it:

  ```sh
  docker compose cp seaport:/data/secret.key .
  ```

- **Blobs.** Back up the bucket, or the `/data/blobs` directory of the `seaport_data` volume.
- **Off the server.** A backup on the same disk is not a backup. Copy dumps to another machine or storage.

> [!WARNING]
> Keep the key and the dumps in different places. A dump without the key is useless, and that is also what makes a stolen dump harmless: the WhatsApp keys, the GitHub, Jira, Slack and email credentials, agent secrets, and in iam mode the IAMKit tokens of browser sessions inside it are sealed.

## Restore

Stop the services that write, restore the dump, and start them again:

```sh
docker compose stop seaport backup
docker compose exec -T postgres pg_restore -U seaport -d seaport --clean --if-exists < backups/seaport-<timestamp>.dump
docker compose start seaport backup
```

Before starting, make sure the new port has the same key: set `SEAPORT_SECRET_KEY` in `.env` to the saved value, or copy the saved `secret.key` into the `seaport_data` volume. Restore the blob store too, so messages show their images.

## If the secret key is lost

You can't recover sealed values without the key. A new key can't open them, so Seaport refuses to generate a new key file while sealed values exist, and stops with a message to restore the old one.

If the key is truly gone, you start over for what it sealed:

- Every WhatsApp dock must be linked again.
- Every GitHub, Jira, Slack and email dock must be connected again: its App, API token and webhook secret, app tokens and signing secret, or mailbox password, are gone.
- Every agent secret must be set again.
- Message attachments (images and files) and harness homes can't be opened. Upload homes again.
- In iam mode, browser sessions are signed out, and people sign in again.

Chats, messages, customs rules and the rest of the database are not sealed, and stay.

## Upgrade

```sh
git pull
docker compose up -d --build
```

Migrations run at start, so there are no manual database steps. Take a backup first. With `--profile hosting`, include it in the command.

Releases are tagged `v*`. Each publishes binaries to GitHub Releases and the base images for hosted agents (`ghcr.io/abraxas-365/seaport-rness` and `seaport-claude-code`), tagged with the version and `latest`. A port gets the base image of its own version by default, so after an upgrade, new builds of agents' machines use the new base. An agent's image is rebuilt when its base changes, since the base is part of the image key. Build ahead of time:

```sh
seaport agents build pm
```

> [!NOTE]
> Coming from a version older than the one that sealed the WhatsApp store (migration 00008), run the release that has it once first (up to commit `e807f94`). It moves linked WhatsApp devices out of clear-text tables, which migration 00012 then drops. 00012 refuses to run while a linked dock is still only in the old tables.

### Upgrading to the agent-centric config

This release removes environments: an agent's [config](../../reference/agent-config.md) now holds its harness and its machine, and the agent is the whole configuration. The upgrade is automatic when the new version starts, but read this first.

**Before you upgrade**

- Take a backup, as always.
- Environments that no agent is hosted on are **dropped**. If you want to keep one, export it first with the *previous* release (`seaport env export`) and later bring it to an agent with `seaport agents config apply`. This release has no `seaport env` command.
- If any environment has secrets, the new version needs `SEAPORT_SECRET_KEY` (or `SEAPORT_SECRET_KEY_FILE`), the same key that sealed them, and refuses to start without it.

**What happens at start**

1. Migration 00045 adds the config's machine and the shared build table.
2. A one-off data step, in a single transaction and under a lock, so several processes starting together run it once, moves each **hosted** agent's environment into its config: a new config version is saved whose profile is the agent's current one plus the environment's harness and machine, created by `seaport upgrade`. Agents that weren't hosted keep their profile.
3. The environment secrets move into the secrets of the agents hosted on them. When an agent already has a secret of that name, the environment's is kept as `<NAME>_ENV` (then `_ENV_2`…), with `-env` off, and the machine's references follow it, so nothing changes for the running agent. Rename or delete these as you like.
4. Migration 00046 drops the environments tables. It refuses to run if environments still have rows and the step didn't mark itself done, so a partial upgrade stops instead of losing data.

Hosted slots that were running are left alone, and restart once they are idle on the new config, which builds the image if it isn't already (the same machine means the same image, shared between agents). Build ahead of time with `seaport agents build <agent>`.

**After you upgrade**

- Check each hosted agent: `seaport agents config show <agent>` and `seaport agents slots`.
- Update scripts and CI that used `seaport env …`: the equivalents are `seaport agents config apply <agent> -f agent.yaml`, `seaport agents build`, `seaport agents host <agent>` (no environment argument), and `seaport agents home …`.
- In `iam` mode, grant the new `seaport:hosting:*` permissions: see [iam mode](iam-mode.md#upgrading-from-environments-to-hosting).
- The `environment.updated`, `environment.deleted` and `environment.build` events are gone; `environment.build` is now `agent.build`.
- A hosted agent works only through its slots, and an agent with live crew tokens can't be hosted. If an agent was connected from people's machines and you want to host it, revoke its tokens first.

### With several processes

Run the same version everywhere. For a rolling deploy, start a new process, then stop the old one. A stopping process hands its docks and deliveries over at once. See [Several processes](several-processes.md).

## Next

- [Deploy](deploy.md)
- [Several processes](several-processes.md)
- [Security model](../../explanation/security-model.md)
