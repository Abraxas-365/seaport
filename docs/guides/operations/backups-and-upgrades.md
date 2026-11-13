# Back up, restore and upgrade

Know what to save, how to bring a port back, and how to move to a new version without losing anything.

A Seaport port keeps its state in three places. Back up all three, or a restore will be missing pieces.

| What | Where | Why it matters |
| :--- | :--- | :--- |
| **Postgres** | The `postgres` service's database. | Docks, chats, messages, customs, agents, environments and sealed secrets. |
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
> Keep the key and the dumps in different places. A dump without the key is useless, and that is also what makes a stolen dump harmless: the WhatsApp keys, the GitHub, Jira and Slack credentials, agent secrets, and in iam mode the IAMKit tokens of browser sessions inside it are sealed.

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
- Every GitHub, Jira and Slack dock must be connected again: its App, API token and webhook secret, or app tokens and signing secret, are gone.
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

Releases are tagged `v*`. Each publishes binaries to GitHub Releases and the base images for hosted agents (`ghcr.io/abraxas-365/seaport-rness` and `seaport-claude-code`), tagged with the version and `latest`. A port gets the base image of its own version by default, so after an upgrade, new builds of environments use the new base. An environment's image is rebuilt when its base changes, since the base is part of the image tag. Build ahead of time:

```sh
seaport env build backend
```

> [!NOTE]
> Coming from a version older than the one that sealed the WhatsApp store (migration 00008), run the release that has it once first (up to commit `e807f94`). It moves linked WhatsApp devices out of clear-text tables, which migration 00012 then drops. 00012 refuses to run while a linked dock is still only in the old tables.

### With several processes

Run the same version everywhere. For a rolling deploy, start a new process, then stop the old one. A stopping process hands its docks and deliveries over at once. See [Several processes](several-processes.md).

## Next

- [Deploy](deploy.md)
- [Several processes](several-processes.md)
- [Security model](../../explanation/security-model.md)
