# Permissions

Every `seaport:*` permission of the admin API, what it allows, and what the roles Admin, Operator and Viewer get in iam mode.

The catalog lives in [iam/permissions.json](../../iam/permissions.json), the single source of truth: the Go constants and the web app's permission list are generated from it. In `iam` mode IAMKit grants these permissions to people through roles. In `none` and `token` modes the one owner holds every permission.

## Catalog

| Permission | Allows |
| :--- | :--- |
| `seaport:docks:read` | View docks (channels) and their link status. Read `dock.updated` and `dock.deleted` events. |
| `seaport:docks:write` | Create, rename, relink, unlink and delete docks. |
| `seaport:conversations:read` | View conversations and the Bandeja (Inbox). Read `conversation.updated` events. |
| `seaport:conversations:write` | Allow, deny and configure conversations (access, group mode, title). |
| `seaport:ships:read` | Read message history and attachments. Read `ship.created`, `ship.updated` and `delivery.changed` events. |
| `seaport:ships:write` | Send messages as the agent would, and inject test messages into a dock. |
| `seaport:customs:read` | View customs (Aduana) policies and rules. Read `customs.updated` events. |
| `seaport:customs:write` | Change customs policies and rules. |
| `seaport:crew:read` | View connected agents (Tripulación). Read `crew.changed` events. |
| `seaport:crew-tokens:read` | View crew tokens (Tokens de agentes). Never their values. |
| `seaport:crew-tokens:write` | Create and revoke crew tokens. |
| `seaport:agents:read` | View agents, the docks they work on, their configs (profile, harness and machine) and secret names, and their model usage. View harness homes. Read `agent.updated`, `agent.deleted` and `agent.takeover` events. |
| `seaport:agents:write` | Create, change and delete agents, assign them to docks, save configs (the profile, harness and machine, including the secrets the machine names), upload harness homes, and set or delete secrets. |
| `seaport:hosting:read` | View builds, the agents the port hosts, their slots and egress denials. Read `agent.build` and `slot.updated` events. |
| `seaport:hosting:write` | Start builds, host and unhost agents, and start, stop and sign in slots. Read slot logs. |
| `seaport:schedules:read` | View schedules (prompts posted into conversations at set times) and preview cron expressions. Read `schedule.*` events. |
| `seaport:schedules:write` | Create, change, run and delete schedules. |
| `seaport:vaults:read` | View vaults (shared memory of agents), their entries, versions and who they are granted to. Read `vault.*` events. |
| `seaport:vaults:write` | Create, change and delete vaults, save, revert and delete their entries, and grant them to agents and docks. |
| `seaport:browser:use` | Open a hosted agent's browser viewer to sign in to sites for it, and clear its login requests. Read `slot.browser` events. |

The permissions that guard each route are in [HTTP API](http-api.md). The permission that reads each event type is in [Events](events.md#event-types).

> [!NOTE]
> A permission is only checked against admin routes. Agents act with a crew token, which isn't a role: it can only reach the crew routes, and only for its own agent. See [Crew protocol](crew-protocol.md) and [Security model](../explanation/security-model.md).

## Roles

The three roles ship in `iam/permissions.json`. `scripts/iamkit-provision.sh` creates them in IAMKit; see [iam mode](../guides/operations/iam-mode.md).

| Permission | Admin | Operator | Viewer |
| :--- | :---: | :---: | :---: |
| `seaport:docks:read` | yes | yes | yes |
| `seaport:docks:write` | yes | | |
| `seaport:conversations:read` | yes | yes | yes |
| `seaport:conversations:write` | yes | yes | |
| `seaport:ships:read` | yes | yes | yes |
| `seaport:ships:write` | yes | yes | |
| `seaport:customs:read` | yes | yes | yes |
| `seaport:customs:write` | yes | | |
| `seaport:crew:read` | yes | yes | yes |
| `seaport:crew-tokens:read` | yes | | |
| `seaport:crew-tokens:write` | yes | | |
| `seaport:agents:read` | yes | yes | yes |
| `seaport:agents:write` | yes | | |
| `seaport:hosting:read` | yes | yes | yes |
| `seaport:hosting:write` | yes | | |
| `seaport:schedules:read` | yes | yes | yes |
| `seaport:schedules:write` | yes | yes | |
| `seaport:vaults:read` | yes | yes | yes |
| `seaport:vaults:write` | yes | yes | |
| `seaport:browser:use` | yes | yes | |

In short:

- **Admin** holds all 20 permissions.
- **Operator** reads everything except crew tokens, and works conversations: it allows and denies chats and sends messages. It also manages schedules and vaults and may open a hosted agent's browser. It can't change docks, customs, agents, tokens or hosting.
- **Viewer** reads docks, conversations, history, customs, the crew, agents, hosting, schedules and vaults. It can't change anything and can't see crew tokens.

You can also grant single permissions or build your own roles in IAMKit; the catalog is what counts.

## Modes

| Mode | Who holds permissions |
| :--- | :--- |
| `none` | The one local owner holds every permission. Loopback only. |
| `token` | The holder of the admin token, or of a session cookie made from it, holds every permission. |
| `iam` | The caller's permissions come from IAMKit. A caller without a permission gets `403 FORBIDDEN` with `missing permission <name>`. |

An object of another organization is always `404`, never `403`. See [HTTP API](http-api.md#errors).

## Changing the catalog

If you build Seaport from source: edit `iam/permissions.json`, then run `go generate ./internal/permission`. That regenerates the Go constants and `web/src/lib/permissions.gen.ts`. Tests fail when they drift, when a permission is named as a string literal in code, when an admin route has no permission, or when a catalog permission is never checked. See [Contributing](../contributing/README.md).
