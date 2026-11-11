# HTTP API

Every route of the port: method, path, the permission it needs, and a sketch of its body and response, with authentication, errors and pagination.

The web app uses this same API. Everything it can do, you can do with any HTTP client. Examples use `http://127.0.0.1:8740`; a real port is at `https://port.example.com`.

## Conventions

### Authentication

Admin routes (`/api/…`) take one of these, depending on `SEAPORT_AUTH`:

| Mode | Credentials |
| :--- | :--- |
| `none` | None. Only loopback callers reach the port: a foreign `Host` or a forwarding header (`X-Forwarded-*`, `Forwarded`) gets `403`. Every write is also checked against cross-origin requests. |
| `token` | `Authorization: Bearer <admin token>`, or the `seaport_session` cookie from `POST /api/v1/session`. |
| `iam` | `Authorization: Bearer ik_key_…` (an IAMKit API key) or a bearer access token, or the `seaport_session` cookie of a browser sign-in. A bearer wins over the cookie. |

- The `seaport_session` cookie in `token` mode is stateless: `<expiry>.<hmac>`, signed with the admin token. It is `HttpOnly`, `SameSite=Strict`, lasts 7 days (`config.SessionTTL`) and stops working when you rotate the admin token. In `iam` mode it is a random id that keys a server-side session.
- Unsafe requests (`POST`, `PUT`, `PATCH`, `DELETE`) that authenticate with a cookie go through cross-origin protection and answer `403` (`cross-origin request rejected`) when they come from another origin. Bearer requests aren't affected.
- A request with no valid credential gets `401` and `WWW-Authenticate: Bearer`.
- `Authorization: Bearer` with a crew token isn't an admin credential. Crew tokens only open the [crew routes](#crew-routes).
- Permissions: in `none` and `token` modes the caller holds every permission. In `iam` mode a route needs the permission listed here, or the port answers `403 FORBIDDEN` (`missing permission seaport:…`). See [Permissions](permissions.md).

### Requests

- Bodies are JSON objects with `Content-Type: application/json`. Another type gets `415 UNSUPPORTED_MEDIA_TYPE`.
- The body may be at most 1 MiB (`httpx.MaxJSONBody`), else `413 PAYLOAD_TOO_LARGE`. An empty body where one is required gets `400` (`request body is required`).
- Unknown fields are rejected with `400` and `details.field`. Send exactly one JSON value.
- A request with a `Content-Encoding` other than identity gets `415`.
- Ids are opaque strings. `X-Request-ID` is echoed back when it matches `^[A-Za-z0-9_-]{1,64}$`, else the port makes one.
- Creating routes answer `201` with a `Location` header: the request path plus `/` plus the new id.
- A route that doesn't exist under `/api/` answers `404` (`route not found`), and a wrong method answers `405` (`METHOD_NOT_ALLOWED`) in the same error shape.
- Every response carries `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and `Cache-Control: no-store`. Files add a sandbox `Content-Security-Policy`. `Strict-Transport-Security` is added over TLS or behind `X-Forwarded-Proto: https`.

### Errors

Every error has the same shape:

```json
{
  "error": {
    "type": "VALIDATION",
    "code": "VALIDATION",
    "message": "name must be 1-32 lowercase letters, digits, - or _, starting with a letter or digit",
    "status_code": 400,
    "details": { "field": "name" },
    "request_id": "7c1e0d5a…"
  }
}
```

| Field | Meaning |
| :--- | :--- |
| `type` | The class of error, below. |
| `code` | A stable machine-readable code. Usually the `type`; some errors have their own. |
| `message` | A sentence for people. Server errors (5xx) carry the generic `an unexpected error occurred`. |
| `status_code` | The HTTP status. |
| `details` | Optional extra data: `field` for a bad field, `quota` and `limit` for quotas, `retry_after_seconds` for rate limits. Never on 5xx. |
| `request_id` | The request's `X-Request-ID`, for logs. |

| Type | Status | When |
| :--- | :--- | :--- |
| `VALIDATION` | 400 | A bad field, a bad id, a malformed body. `details.field` names the field. |
| `AUTHORIZATION` | 401 | No valid credential. |
| `NOT_FOUND` | 404 | The thing doesn't exist, or belongs to another organization (never `403`). |
| `CONFLICT` | 409 | A name is taken, or an edit used a stale `base` version. |
| `BUSINESS` | 422 | The request is well-formed but the state forbids it. |
| `EXTERNAL` | 502 | A channel or backend refused: a failed WhatsApp send. |
| `INTERNAL` | 500 | A bug or an outage. |

Codes with a status of their own:

| Code | Status | Meaning |
| :--- | :--- | :--- |
| `FORBIDDEN` | 403 | You are signed in but lack the permission, or the origin or host is refused. |
| `QUOTA_EXCEEDED` | 422 | A per-organization limit was reached. `details.quota` names it and `details.limit` gives the value. See below. |
| `RATE_LIMITED` | 429 | Too many requests. The `Retry-After` header and `details.retry_after_seconds` say how long to wait. |
| `METHOD_NOT_ALLOWED` | 405 | The path exists, not with that method. |
| `PAYLOAD_TOO_LARGE` | 413 | Body over the limit. |
| `UNSUPPORTED_MEDIA_TYPE` | 415 | Wrong `Content-Type` or `Content-Encoding`. |
| `DOCK_MOVED` | 409 | A dock changed process while a request was routed to it. The port retries on its own first; if you still get it, try again. |

Quota names: `docks`, `crew_connections`, `crew_tokens`, `agents`, `agent_secrets`, `environments`, `slots` and `homes`. The values are in [Limits](limits.md) and [Configuration](configuration.md#quotas).

**Rate limits.** Two rates exist:

- The API rate (`SEAPORT_API_RATE`, `SEAPORT_API_BURST`) is a token bucket per caller (organization, role, subject and credential). It covers `/api/`, `/crew/ws`, `/mcp`, `/crew/profile`, `/crew/attachments`, `/crew/reply`, `/crew/workspace` and `/crew/github/token`, after authentication. The gateway and the public routes aren't limited. The bucket is per process.
- The outbound limits (`SEAPORT_SEND_*`) refuse a reply when a dock, conversation or organization sent too much lately.

Both answer `429` with `Retry-After` in seconds. Wait that long.

### Pagination

List routes take `limit` and `offset` and answer an envelope:

```json
{ "items": [ … ], "page": { "total": 41, "limit": 20, "offset": 0 } }
```

| Parameter | Default | Range |
| :--- | :--- | :--- |
| `limit` | 20 | 1 to 100 (a larger value is cut to 100). A non-number or less than 1 is `400` (`details.field` = `limit`). |
| `offset` | 0 | 0 to 10000. Outside that is `400`. |

`page.total` is the number of matches, so loop until `offset + limit >= total`. Routes that answer a short fixed list (`{"agents": […]}`, `{"versions": […]}`, `{"secrets": […]}`) aren't paginated.

## Public routes

No credentials, and nothing about any organization.

| Method | Path | Response |
| :--- | :--- | :--- |
| `GET` | `/healthz` | `200` `{"status":"ok","version":"0.7.0"}`. Use it as a liveness probe. |
| `GET` | `/api/v1/config` | `200` `{"auth":"token","version":"0.7.0"}`: how the web app must sign in. |
| `GET` | `/api/v1/session` | `200` the session state (below), also when signed out. Never `401`. |
| `POST` | `/api/v1/session` | `token` mode: body `{"token":"…"}`; sets the cookie and answers the session state. A wrong token is `401` (`invalid token`). |
| `GET` | `/api/v1/session/login` | `iam` mode only: starts the browser sign-in and redirects to IAMKit. Query: `next` (a path to come back to, at most 2048 characters), `organization`, `switch=1` (pick another organization). |
| `GET` | `/auth/callback` | `iam` mode only: OAuth code and PKCE callback. It sets the cookie and redirects. |
| `GET` | `/github/apps/created` | GitHub's redirect after it created a dock's App (`code`, `state`). Seaport trades the code for the App's credentials and redirects to its install page. A bad or expired `state` is `401`. |
| `GET` | `/github/apps/installed` | GitHub's redirect after an install (`installation_id`, `state`). Seaport checks the installation with GitHub and redirects to the dock's page (`?github=installed`, or `?github_error=…`). |
| `POST` | `/github/webhook/{dock}` | The App's webhook, signed with its secret (`X-Hub-Signature-256`, else `401`; body up to 5 MiB). `200` for `ping` and `installation` events, `202` `{"status":"received","ship_id":"…","created":true}` for a message that called an agent, `202` `{"status":"ignored"}` for everything else. `404` for a dock with no App. |
| `POST` | `/github/actions/{dock}` | A message from a GitHub Actions workflow. Bearer: the job's OIDC token, issued for this port (audience = `SEAPORT_PUBLIC_URL`; signature, issuer and expiry checked, else `401`). The token's repository must be served by an active installation of the dock's App, else `403`. Body `{"text":"…","number":12,"agent":"qa","id":"step-1"}`: `number` defaults to the pull request of the workflow's `refs/pull/N/merge` ref, `agent` must be on the dock's team, `id` keeps a retried step from sending twice. `202` `{"status":"queued"\|"held","ship_id","created","conversation_id","chat"}`. See [GitHub](../guides/github.md#trigger-an-agent-from-a-workflow). |
| `POST` | `/jira/webhook/{dock}` | A Jira webhook or Automation rule for a Jira dock. Proof (else `401`): `X-Hub-Signature: sha256=<hex HMAC-SHA256 of the body>` with the dock's webhook secret, or `Authorization: Bearer <secret>`; body up to 5 MiB. `202` `{"status":"received","ship_id":"…","created":true}` for a message that called the account or an agent, `{"status":"recorded",…}` for a comment of the account itself, `{"status":"ignored"}` for everything else. `404` for a dock with no site. See [Jira](../guides/jira.md). |
| `GET` | `/slack/oauth/callback` | Slack's redirect after a dock's app install (`code`, `state`; or `error`). Seaport trades the code, keeps the bot token and redirects to the dock's page (`?slack=installed`, or `?slack_error=…`). A bad or expired `state` is `401`. |
| `POST` | `/slack/events/{dock}` | The app's Events API endpoint, signed with its signing secret (`X-Slack-Signature` v0 HMAC over `X-Slack-Request-Timestamp`, at most 5 minutes old, else `401`; body up to 3 MiB). `url_verification` is answered with its `challenge` even before the dock has an app. Otherwise it answers at once (Slack waits 3 seconds): `200` `{"status":"accepted"}` for a message for the crew, which Seaport then reads (names, images) and stores in the background, so the ship appears moments later (a Slack retry is dropped by the message's external id); `200` `{"status":"ignored"}` for everything else. `404` for a dock with no app. See [Slack](../guides/slack.md). |

Session state:

```json
{
  "authenticated": true,
  "expires_at": "2025-01-22T10:30:00Z",
  "subject": "admin",
  "name": "Ana",
  "email": "ana@example.com",
  "organization_id": "…",
  "organization_name": "Example Co",
  "permissions": ["seaport:docks:read", "…"]
}
```

A signed-out browser gets `{"authenticated": false, "permissions": []}`. Fields that are empty are left out.

Admin route, not public:

| Method | Path | Permission | Response |
| :--- | :--- | :--- | :--- |
| `DELETE` | `/api/v1/session` | none (a valid session) | Signs out. `204` in `token` mode. In `iam` mode `200` `{"logout_url":"…"}`: send the browser there to end the IAMKit session too. |

Everything else that isn't `/api/…` or a crew route is the web app, served from the binary.

## Admin routes

All under `/api/v1`. "Read" and "write" below are the permissions of [Permissions](permissions.md), such as `seaport:docks:read`.

### Docks

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/docks` | `docks:read` | Paginated docks. |
| `POST` | `/docks` | `docks:write` | Body `{"channel":"whatsapp"\|"api"\|"github"\|"jira"\|"slack","name":"…"}`. `201` with the dock. `422 QUOTA_EXCEEDED` (`docks`) over the limit. |
| `GET` | `/docks/{id}` | `docks:read` | The dock, with `qr_code` while a WhatsApp dock is linking. |
| `PATCH` | `/docks/{id}` | `docks:write` | Body `{"name":"…"}`. The dock. |
| `DELETE` | `/docks/{id}` | `docks:write` | `204`. Deletes its conversations and ships. |
| `POST` | `/docks/{id}/relink` | `docks:write` | Starts pairing again. The dock. |
| `POST` | `/docks/{id}/unlink` | `docks:write` | Logs the device out (a GitHub dock: uninstalls and forgets its App; a Jira dock: forgets its site, token and secret; a Slack dock: uninstalls the app it created and forgets it). The dock. |
| `POST` | `/docks/{id}/ships` | `ships:write` | Injects an inbound message into an `api` dock. See below. |

A dock:

```json
{
  "id": "…",
  "channel": "whatsapp",
  "name": "Support line",
  "address": "34600000000:12@s.whatsapp.net",
  "status": "linked",
  "created_at": "2025-01-15T10:30:00Z",
  "updated_at": "2025-01-15T10:31:00Z",
  "qr_code": "2@…"
}
```

`status` is `linking`, `linked`, `logged_out` or `failed`. `qr_code` appears only while linking.

**Inject a message** (`POST /docks/{id}/ships`) only works on `api` docks (else `422`):

```json
{
  "chat_id": "chat-1",
  "chat_kind": "direct",
  "chat_title": "Ana",
  "message_id": "m-1001",
  "sender_id": "+34600000000",
  "sender_name": "Ana",
  "text": "Hello",
  "sent_at": "2025-01-15T10:30:00Z",
  "chat_handles": [],
  "sender_handles": [],
  "addressed": false,
  "images": [{ "name": "photo.png", "data": "<base64>" }]
}
```

- Required: `chat_id`, `message_id`, and `text` (or `images`). `chat_kind` is `direct` (default) or `group`. `sent_at` defaults to now. At most 16 handles each.
- `chat_handles` and `sender_handles` are the identities customs matches; `chat_id` and `sender_id` are used when empty. `addressed` says a group message mentions the account.
- `images` are base64 inside the JSON, so the whole body stays under 1 MiB.
- Answer: `201` with the ship and `"duplicate": false`. A repeated `message_id` answers `200` with `"duplicate": true` and stores nothing.

See [API docks](../guides/api-docks.md).

### GitHub docks

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/docks/{id}/github` | `docks:read` | The dock's App and installations (below). `422` for a dock of another channel. |
| `POST` | `/docks/{id}/github/manifest` | `docks:write` | Body `{"organization":"acme","name":"Seaport QA"}`, both optional (no organization: the user's account; name: `Seaport <dock name>`, at most 34 characters). `{"action_url":"…","manifest":"…"}`: the web posts `manifest` as a form field to `action_url` on GitHub. The URL carries a signed state valid for 1 hour. |

```json
{
  "app": {
    "app_id": 123456,
    "slug": "seaport-qa",
    "name": "Seaport QA",
    "owner": "acme",
    "owner_type": "Organization",
    "html_url": "https://github.com/apps/seaport-qa",
    "created_at": "2025-01-15T10:30:00Z"
  },
  "installations": [
    { "id": 9, "account": "acme", "account_type": "Organization", "suspended": false, "created_at": "2025-01-15T10:31:00Z" }
  ],
  "webhook_url": "https://port.example.com/github/webhook/<dock-id>",
  "reachable": true,
  "install_url": "https://github.com/apps/seaport-qa/installations/new?state=…"
}
```

`app` is `null` until the App is created. `reachable` is `false` when `SEAPORT_PUBLIC_URL` is plain `http` or loopback (GitHub couldn't deliver webhooks). No secret of the App is ever answered. See [GitHub](../guides/github.md).

### Jira docks

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/docks/{id}/jira` | `docks:read` | The dock's site (below). Never the token or the secret. `422` for a dock of another channel. |
| `PUT` | `/docks/{id}/jira` | `docks:write` | Body `{"site":"https://example.atlassian.net","email":"bot@example.com","api_token":"…"}`. Seaport checks the site against `SEAPORT_JIRA_HOSTS` (`400` if not allowed) and the credentials with `GET /rest/api/3/myself` (`422` if Jira refuses them), stores them sealed and links the dock (address = the site's host). The first link makes the webhook secret and answers it in `secret`; linking again keeps it and answers none. |
| `POST` | `/docks/{id}/jira/secret` | `docks:write` | Makes a new webhook secret (the old one stops working) and answers it in `secret`. |

```json
{
  "site": {
    "site": "https://example.atlassian.net",
    "account_id": "5b10a2844c20165700ede21g",
    "display_name": "Seaport Bot",
    "email": "bot@example.com"
  },
  "webhook_url": "https://port.example.com/jira/webhook/<dock-id>",
  "reachable": true,
  "secret": "…",
  "hosts": ["*.atlassian.net"]
}
```

`site` is `null` until the dock is linked. `secret` appears only in the answers that made it (first link, rotate). `reachable` is `false` when `SEAPORT_PUBLIC_URL` is plain `http` or loopback. `hosts` is the site allow-list. See [Jira](../guides/jira.md).

### Slack docks

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/docks/{id}/slack` | `docks:read` | The dock's app, its manifest and URLs (below). `422` for a dock of another channel. |
| `POST` | `/docks/{id}/slack/create` | `docks:write` | Flow A. Body `{"config_token":"xoxe.xoxp-…","name":"Seaport Support"}` (`name` optional, at most 35 characters). Creates the app from the manifest with Slack's `apps.manifest.create` (the token is used once and not kept) and answers `{"install_url":"…"}`: Slack's OAuth page, with a signed state valid for 1 hour, whose redirect lands on `/slack/oauth/callback`. Creating an app on a dock that had one replaces it and puts the dock back to `linking` until the new app is installed. `422` when Slack refuses the token. |
| `POST` | `/docks/{id}/slack/install` | `docks:write` | Answers `{"install_url":"…"}` for an app Seaport created (flow A) that is not installed yet, or to install it again: a fresh signed state valid for 1 hour. It is a separate call because finishing the install replaces the bot token. `422` when the dock has no app or the app was not created by Seaport. |
| `POST` | `/docks/{id}/slack/connect` | `docks:write` | Flow B. Body `{"signing_secret":"…","bot_token":"xoxb-…"}`. Checks the token with `auth.test`, keeps both sealed, links the dock and answers like `GET`. `422` for a token Slack refuses. |

```json
{
  "app": {
    "app_id": "A0123ABCD",
    "name": "Seaport Support",
    "team_id": "T0123ABCD",
    "team_name": "Acme",
    "team_url": "https://acme.slack.com/",
    "bot_user_id": "U0123ABCD",
    "installed": true,
    "managed": true,
    "created_at": "2025-01-15T10:30:00Z"
  },
  "manifest": { "display_information": { "name": "Seaport Support" } },
  "events_url": "https://port.example.com/slack/events/<dock-id>",
  "redirect_url": "https://port.example.com/slack/oauth/callback",
  "reachable": true
}
```

`app` is `null` until an app is connected; `installed` is `false` for an app created but not yet installed. `manifest` is the full app manifest (abridged above) for flow B. `managed` is `true` for an app Seaport created (flow A), the only kind it can install (`POST …/slack/install`). `reachable` is `false` when `SEAPORT_PUBLIC_URL` is plain `http` or loopback. No token or secret is ever answered. See [Slack](../guides/slack.md).

### Conversations

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/conversations` | `conversations:read` | Paginated conversations. Filters `status` (`allowed`, `pending`, `denied`) and `dock_id`. |
| `GET` | `/conversations/{id}` | `conversations:read` | The conversation. |
| `PATCH` | `/conversations/{id}` | `conversations:write` | Body with any of `access` (`auto`, `allow`, `deny`), `group_mode` (`mentions`, `all`, or empty to inherit the dock's) and `title`. The conversation. |
| `GET` | `/conversations/{id}/ships` | `ships:read` | Paginated ships (messages), newest first. |
| `GET` | `/conversations/{id}/turns` | `ships:read` | Paginated [turn summaries](events.md#turn) (the tools an agent called on each delivery), newest first. |
| `POST` | `/conversations/{id}/ships` | `ships:write` | Body `{"text":"…"}`. Sends a message as a person would. `201` with the ship. `403` if the conversation isn't allowed. |
| `GET` | `/inbox` | `conversations:read` | Paginated conversations with a preview. Same filters as `/conversations`. |
| `GET` | `/attachments/{id}` | `ships:read` | The file, with its sniffed type: an image, or a document (a file an agent sent) as a download (`Content-Disposition: attachment`). |

A conversation:

```json
{
  "id": "…",
  "dock_id": "…",
  "channel": "whatsapp",
  "external_id": "34600000000@s.whatsapp.net",
  "kind": "direct",
  "title": "Ana",
  "access": "auto",
  "group_mode": "mentions",
  "status": "allowed",
  "handles": ["+34600000000"],
  "last_activity_at": "2025-01-15T10:30:00Z",
  "created_at": "2025-01-15T10:00:00Z",
  "updated_at": "2025-01-15T10:30:00Z"
}
```

`kind` is `direct`, `group` or `self`. A ship:

```json
{
  "id": "…",
  "conversation_id": "…",
  "direction": "in",
  "external_id": "3EB0…",
  "sender_id": "+34600000000",
  "sender_name": "Ana",
  "text": "Hello",
  "status": "queued",
  "agent_id": "…",
  "hold_reason": "",
  "delivery_id": "…",
  "error": "",
  "sent_at": "2025-01-15T10:30:00Z",
  "created_at": "2025-01-15T10:30:01Z",
  "attachments": [{ "id": "…", "kind": "image", "mime": "image/png", "size": 48213, "name": "photo.png", "sha256": "…" }]
}
```

`direction` is `in` or `out`. Inbound `status` is `held`, `queued`, `claimed` or `handled`; outbound is `pending`, `sent` or `failed`. Empty fields are left out. `/inbox` items add `last_ship` (a ship, omitted without `ships:read`), `held`, `queued` and `in_flight`.

Attachment routes answer the raw bytes with `Cache-Control: private, no-store`, `X-Content-Type-Options: nosniff` and `Content-Security-Policy: default-src 'none'; sandbox`. Only images are stored. See [Messages and images](../guides/messages-and-images.md).

### Customs

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/docks/{id}/customs` | `customs:read` | `{"policy":{…},"rules":[…]}`. |
| `PATCH` | `/docks/{id}/customs/policy` | `customs:write` | Body with any of `default` (`ask`, `allow`, `deny`) and `group_mode` (`mentions`, `all`). The customs. |
| `POST` | `/docks/{id}/customs/rules` | `customs:write` | Body `{"effect":"allow"\|"deny","pattern":"+34600000000"}`. `201` with the rule. |
| `DELETE` | `/customs/rules/{id}` | `customs:write` | `204`. |

A rule is `{"id","dock_id","effect","pattern","created_at"}`. A pattern has at most 256 characters, and a dock at most 1000 rules. See [Customs](../guides/customs.md).

### Agents

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/agents` | `agents:read` | Paginated agents. |
| `POST` | `/agents` | `agents:write` | Body `{"name":"…","description":"…"}`. `201` with the agent. `422 QUOTA_EXCEEDED` (`agents`). `409` when the name is taken. |
| `GET` | `/agents/{id}` | `agents:read` | The agent. |
| `PATCH` | `/agents/{id}` | `agents:write` | Body with any of `name` and `description`. The agent. |
| `DELETE` | `/agents/{id}` | `agents:write` | `204`. Revokes its tokens, disconnects it, holds what waits for it. |
| `GET` | `/docks/{dock}/agents` | `agents:read` | `{"agents":[{"agent_id","name","default"}]}`: the dock's team. |
| `PUT` | `/docks/{dock}/agents/{id}` | `agents:write` | Optional body `{"default":true}`. Assigns the agent. The agent. |
| `DELETE` | `/docks/{dock}/agents/{id}` | `agents:write` | Takes the agent off the dock. The agent. |
| `GET` | `/agents/{id}/profile` | `agents:read` | The current [profile](agent-profile.md). |
| `PUT` | `/agents/{id}/profile` | `agents:write` | Body: profile fields plus `base`. Saves a new version. The profile. |
| `GET` | `/agents/{id}/profile/versions` | `agents:read` | `{"versions":[{"version","created_by","created_at"}]}`. |
| `GET` | `/agents/{id}/profile/versions/{version}` | `agents:read` | A past profile version. |
| `GET` | `/agents/{id}/secrets` | `agents:read` | `{"secrets":[{"name","env","updated_at"}]}`. Never values. |
| `PUT` | `/agents/{id}/secrets/{name}` | `agents:write` | Body `{"value":"…","env":false}`. The secret's name and flags. |
| `DELETE` | `/agents/{id}/secrets/{name}` | `agents:write` | `204`. |
| `GET` | `/agents/{id}/usage` | `agents:read` | Query `days` (default 30, at most 366). `{"days":[{"day","provider","model","requests","input_tokens","output_tokens","cache_write_tokens","cache_read_tokens"}]}`. |

An agent:

```json
{
  "id": "…",
  "name": "support",
  "description": "Answers customers",
  "created_by": "Ana",
  "created_at": "2025-01-15T10:00:00Z",
  "updated_at": "2025-01-15T10:00:00Z",
  "profile_version": 3,
  "docks": [{ "dock_id": "…", "default": true }]
}
```

A name has 1 to 32 lowercase letters, digits, `-` or `_`, starting with a letter or digit. A description has at most 1000 characters. See [Agents and routing](../guides/agents-and-routing.md).

### Crew

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/crew` | `crew:read` | `{"agents":[…]}`: every connected connector, in every process. |
| `GET` | `/crew/tokens` | `crew-tokens:read` | Paginated tokens. Filter `agent_id`. |
| `POST` | `/crew/tokens` | `crew-tokens:write` | Body `{"name":"…","agent_id":"…","expires_at":"2026-01-01T00:00:00Z"}`. `201` with the token and its value, **once**. |
| `DELETE` | `/crew/tokens/{id}` | `crew-tokens:write` | `204`. Closes the token's live connections. |

A crew token:

```json
{
  "id": "…",
  "name": "laptop",
  "agent_id": "…",
  "agent_name": "support",
  "hint": "sp_crew_AbCd",
  "created_by": "Ana",
  "created_at": "2025-01-15T10:00:00Z",
  "expires_at": null,
  "last_used_at": null,
  "token": "sp_crew_…"
}
```

`token` appears only in the create answer, with `Cache-Control: no-store`; the port keeps its SHA-256 only. `agent_id` is required. `expires_at` is optional, in the future and within 5 years. At most 100 active tokens per organization (`422 QUOTA_EXCEEDED`, `crew_tokens`). `slot` shows on tokens a hosted slot uses.

A crew status agent: `session`, `agent_id`, `agent_name`, `name`, `concurrency`, `steer`, `profile_version`, `outdated`, `remote`, `connected_at` and `deliveries` (`id`, `conversation_id`, `ships`, `since`).

### Hosting

Environments, builds, homes and hosted agents. Permissions are `environments:read` and `environments:write`. A body that names secrets (repo credentials, model keys) also needs `agents:write`.

| Method | Path | Permission | Body and response |
| :--- | :--- | :--- | :--- |
| `GET` | `/environments` | `environments:read` | Paginated environments. |
| `POST` | `/environments` | `environments:write` | Body `{"name","spec"}`. `201` with the environment. `422 QUOTA_EXCEEDED` (`environments`). |
| `GET` | `/environments/{id}` | `environments:read` | The environment. `{id}` is an id or a name. |
| `PUT` | `/environments/{id}` | `environments:write` | Body `{"base","spec"}`. Saves a new version. The environment. |
| `DELETE` | `/environments/{id}` | `environments:write` | `204`. Refused while agents are hosted on it. |
| `GET` | `/environments/{id}/versions` | `environments:read` | `{"versions":[{"version","created_by","created_at"}]}`. |
| `GET` | `/environments/{id}/versions/{version}` | `environments:read` | A past version. |
| `POST` | `/environments/{id}/build` | `environments:write` | Optional body `{"version":0,"force":false}`. `202` with the build. |
| `GET` | `/environments/{id}/build` | `environments:read` | The build of the current version. |
| `GET` | `/environments/{id}/versions/{version}/build` | `environments:read` | The build of that version. |
| `POST` | `/homes?harness=rness\|claude-code` | `environments:write` | Raw `application/x-tar` or `application/gzip` body, at most 25 MiB. `201` with the home. |
| `GET` | `/homes/rules` | `environments:read` | What each harness's home may hold. |
| `GET` | `/homes/{digest}` | `environments:read` | The home's digest, harness, files and size. |
| `GET` | `/homes/{digest}/archive` | `environments:read` | The home as a tar. |
| `GET` | `/homes/{digest}/files/{path…}` | `environments:read` | One file of the home. |
| `GET` | `/hosting` | `environments:read` | `{"agents":[…],"runtime":"off"\|"docker"}`: every hosted agent. |
| `GET` | `/agents/{id}/hosting` | `environments:read` | The agent's hosting: `agent_id`, `environment_id`, `slots`, `updated_by`, `updated_at`. |
| `PUT` | `/agents/{id}/hosting` | `environments:write` | Body `{"environment":"<id or name>","slots":1}`. Hosts or moves the agent. |
| `DELETE` | `/agents/{id}/hosting` | `environments:write` | `204`. Running slots must be stopped first. |
| `GET` | `/agents/{id}/hosting/egress` | `environments:read` | `{"denials":[{"slot","host","count","last_seen"}]}`. |
| `POST` | `/agents/{id}/hosting/slots/{slot}/start` | `environments:write` | `202` with the slot. |
| `POST` | `/agents/{id}/hosting/slots/{slot}/stop` | `environments:write` | `202` with the slot. |
| `GET` | `/agents/{id}/hosting/slots/{slot}/logs` | `environments:write` | Query `lines` (default 300, at most 2000). `{"slot","log"}`. |
| `GET` | `/agents/{id}/hosting/slots/{slot}/account` | `environments:read` | `{"slot","supported","logged_in","method","subscription"}`. |
| `POST` | `/agents/{id}/hosting/slots/{slot}/login` | `environments:write` | Starts a subscription sign-in. `202`. Experimental. |
| `GET` | `/agents/{id}/hosting/slots/{slot}/login` | `environments:write` | The sign-in's state: `id`, `slot`, `state`, `url`, `detail`. |
| `POST` | `/agents/{id}/hosting/slots/{slot}/login/code` | `environments:write` | Body `{"code":"…"}`. `202`. |

An environment: `id`, `name`, `version`, `spec`, `created_by`, `created_at`, `updated_at` and `agents` (how many are hosted on it). The spec's fields are in [Environment spec](environment-spec.md). A slot: `index`, `state` (`stopped`, `starting`, `running`, `stopping`, `failed`), `detail`, `image`, `environment_version`, `started_at`, `active_at`, `updated_at`. A build: `environment_id`, `version`, `state` (`building`, `ready`, `failed`), `base`, `image`, `detail`, `log` (the tail, on the build route only), `started_at`, `finished_at`, `updated_at`.

Builds, slots and hosted runs need `SEAPORT_RUNTIME=docker`; with it `off` they are refused. See [Hosted agents](../guides/hosting/hosted-agents.md) and [Harness homes](../guides/hosting/harness-homes.md).

### Events

| Method | Path | Permission | Response |
| :--- | :--- | :--- | :--- |
| `GET` | `/events` | none of its own | A Server-Sent Events stream. See [Events](events.md). |

## Crew routes

These routes are for agents, not for people. They authenticate with a **crew token** (`Authorization: Bearer sp_crew_…`), which belongs to one agent and one organization. A missing or unknown token gets `401` (`missing or invalid token`). They aren't under `/api/`, and an admin credential doesn't open them (in `none` mode they need no token).

| Method | Path | Response |
| :--- | :--- | :--- |
| `GET` | `/crew/ws` | WebSocket upgrade: the agent's connection. See [Crew protocol](crew-protocol.md). |
| `POST` | `/mcp` | The MCP server (streamable HTTP). See [MCP tools](mcp-tools.md). |
| `GET` | `/crew/profile` | The agent's kit: its profile with secrets resolved, `env` and `revision`. See [Agent profile](agent-profile.md). Query `agent=<name>` names the agent in `none` mode; with a token it must match. `Cache-Control: no-store`. |
| `GET` | `/crew/attachments/{id}` | An attachment's bytes (an image, or a document as a download). Only for conversations on the agent's docks, else `404`. |
| `POST` | `/crew/reply` | A reply as the agent, with files. Body `{"conversation_id","text","files":[{"name","data"}]}` (`data` base64; body up to about 35 MiB). `201` `{"ship_id","status"}`. Agent tokens only (`403` without an agent). Same rules as the MCP `reply` tool. |
| `GET` | `/crew/workspace` | A hosted slot's plan for `/workspace`: `slot`, `harness`, `repo_config`, `persistent`, `repos`, `sign_in`, with repo credentials resolved. Only a slot's own token gets it; any other is `403`. |
| `POST` | `/crew/github/token` | A GitHub App token for one repository. Body `{"repository":"owner/repo","host":"github.com"}` (`host` optional: another host than the port's GitHub is `404`). `200` `{"token","expires_at","repository","host","api_url","app"}`, `Cache-Control: no-store`. Only an agent's crew token, and only for a repository where the App of one of its GitHub docks is installed and active; else `404` (`403` without an agent: `none` mode). See [GitHub](../guides/github.md#act-on-github-as-the-app). |
| `*` | `/gateway/anthropic/…` | The model gateway for hosted slots. |

The gateway is for hosted slots only: it takes the slot token as the bearer or as `x-api-key`, drops the caller's own `Authorization`, adds the real provider key from the agent's secrets and forwards:

- `/v1/messages` and `/v1/messages/count_tokens`,
- `/v1/models` and what is under it,
- `/v1/files` and what is under it.

Any other path under it answers `404`. It streams responses and records the token usage of successful Messages calls (`GET /api/v1/agents/{id}/usage`). Errors come back in Anthropic's error shape. It isn't covered by the API rate limit. Where it forwards to is `SEAPORT_GATEWAY_ANTHROPIC_URL`. See [Network](../guides/hosting/network.md).

> [!NOTE]
> `/crew/profile`, `/crew/workspace` and `/crew/github/token` hand out secrets. They only answer the crew credential of the agent itself, never an admin credential. See [Security model](../explanation/security-model.md).
