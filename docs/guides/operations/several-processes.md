# Run several processes

Run more than one Seaport process against the same Postgres, for replicas behind a load balancer or a rolling deploy.

One process is enough for most ports. You want several when you deploy without downtime (start the new process, then stop the old one) or need more capacity. Any process answers any request, so the load balancer needs no sticky sessions.

## Give every process the same configuration

- The same `DATABASE_URL`.
- The same `SEAPORT_SECRET_KEY`. Set it explicitly: if each process generated its own key file, they couldn't open each other's sealed values.
- The same `SEAPORT_ADMIN_TOKEN` in token mode.
- One shared `SEAPORT_BLOB_URL`, an S3 bucket or compatible store. With local directories, each process sees only the files it received.

## What is shared through Postgres

| What | How it works across processes |
| :--- | :--- |
| **Events** | Every process appends to one event log and listens for new events, so a client on any process streams the events of all of them. A client can resume on a different process with its last event id. The log keeps events for an hour; a client with an older id is told to reload. |
| **Crew presence** | Each connection of an agent is a row in Postgres. The crew status and the agent quota count every process. A message arriving at one process wakes agents connected to any other, and revoking a token closes its connections everywhere. |
| **Dock ownership** | A WhatsApp number keeps a single connection, so each WhatsApp dock runs in one process at a time: the holder of its lease in Postgres. Other processes pass it their replies, unlinks and relinks, and show the pairing QR code. `api`, GitHub, Jira and Slack docks need no owner: they hold no connection, so any process takes their webhooks and sends their replies. |
| **Hosted agents** | Every process runs the supervisor. Each step is a conditional change in Postgres, so only one process starts or stops a given slot. |
| **Customs** | Decisions run under a per-dock lock held in Postgres, shared by every process. |

### When a process stops

- **Clean shutdown.** The process hands its agents' deliveries back at once, and hands its docks over: it finishes work in flight, disconnects each dock and releases the leases. Another process reconnects them.
- **A crash.** The dead process stops renewing its rows. Within about half a minute another process notices and gives its deliveries to the remaining agents. Its docks are lost for about 30 seconds (the dock lease) until another process reconnects them. A WhatsApp pairing in progress on it is lost: link the dock again. Until its connections are cleaned up, the dead process's rows still count against the agent quota (about 30 seconds).

Dock ownership is first come, first served, not balanced: a process that restarts first may end up with all the docks.

## Postgres connections

Seaport uses `LISTEN`/`NOTIFY` and session advisory locks. Both need a direct connection or a pooler in **session** mode. A pooler in transaction mode, such as PgBouncer's, breaks them: don't put one between Seaport and Postgres.

## Not shared

The API rate limit is per process, in memory. With N processes, a caller may get up to N times `SEAPORT_API_RATE`. Quotas (docks, crew tokens, live agent connections) and the outbound send limits are counted in Postgres, so they hold across processes.

## Next

- [Deploy](deploy.md)
- [Backups and upgrades](backups-and-upgrades.md)
- [Architecture](../../architecture.md)
