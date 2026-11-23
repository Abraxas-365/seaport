# Wake agents from any system with webhooks

A `webhook` dock is an HTTPS endpoint any system can POST JSON to (an alerting tool, a CI job, a payment provider, a cron): each call wakes an agent of the dock's team with a message you shape with a template.

A webhook dock only triggers. Nothing is posted back to the caller: the dock never takes replies, so the agent answers on another door of the same thread (a Slack or `api` conversation you [link](linked-conversations.md)) or stays silent. Customs decides whether a conversation may wake an agent, the dock's team decides which one, and the agent sees the message like any other.

> [!NOTE]
> The caller must reach your port: use a public `https` `SEAPORT_PUBLIC_URL` (see [Deploy](operations/deploy.md)) and the [secret key](../reference/configuration.md#secret-key), which seals the dock's secret. For a port on your laptop, put a tunnel in front of it and set `SEAPORT_PUBLIC_URL` to the tunnel's address.

## Create the dock

1. Create it: **Muelles → Nuevo muelle → Webhook** in the web app, or `seaport dock add webhook -name alerts`. The dock is **linked** at once.
2. Make its secret: **Generar secreto** on the dock's page, or `seaport dock webhook <dock-id> -rotate`. Copy it: Seaport stores it sealed and shows it only once. Generating another one replaces it, and the old one stops working.
3. Give the dock a team and let its conversations in: `seaport agents assign <agent> <dock-id> -default`, then allow its conversations in [customs](customs.md) (`seaport customs -dock <dock-id> default allow` for a dock only your systems know).

The webhook URL is `<SEAPORT_PUBLIC_URL>/hooks/<dock-id>`.

## Call it

Send a JSON object, or an array of up to 20 objects (one message per element), with the secret in any one of these:

| Proof | How |
| :--- | :--- |
| Bearer | `Authorization: Bearer <secret>` |
| Token header | `X-Seaport-Token: <secret>` |
| Signature | `X-Seaport-Signature: sha256=<hex HMAC-SHA256 of the body>`, keyed with the secret |
| Query string | `?token=<secret>` (last resort: URLs end up in logs) |

```sh
curl -X POST "https://port.example.com/hooks/<dock-id>" \
  -H "Authorization: Bearer $WEBHOOK_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"event":"issue.created","issue":{"id":"WEB-1","title":"Checkout fails","level":"error"}}'
```

The answer is `202` with `{"accepted":1,"ignored":0}`. A message the dock already has (same external id: a provider's retry) counts as `ignored`. A wrong secret and an unknown dock are the same `404`; a body that isn't a JSON object or array is `415`.

A dock takes 60 calls a minute (a burst of 60, then one a second); more are `429` with `Retry-After`, wrong secrets included. Each event of an array is stored on its own, so if the call fails part-way (an error answer, a timeout) the earlier events are already stored. Retry the whole body: the stored ones come back as `ignored`.

## Shape the message with a template

Without a template the agent gets one conversation per dock (`webhook`) and the body as an indented JSON block. A template maps the body to the fields of a message. Each field is a [Go text template](https://pkg.go.dev/text/template) run over the body:

| Field | Meaning | Default |
| :--- | :--- | :--- |
| `chat_id` | the conversation | `webhook` |
| `chat_title` | its title | the chat id |
| `external_id` | dedups retries | `hook-<SHA-256 of the body>-<UTC minute>` |
| `sender_id` | who sends | `webhook` |
| `sender_name` | its display name | the dock's name |
| `text` | the message | the body as a fenced JSON block |
| `tags` | comma separated extra handles that [customs rules](customs.md#rules) can match | none (at most 10) |
| `agent` | agent to call: the text is prefixed with `@name ` | the team's routing |

Without an `external_id` the default is the body's hash plus the UTC minute, so a provider's retry within the minute is a duplicate, while an identical alert a minute later still arrives. If the provider's events have an id or a timestamp, put it in `external_id` (`{{.issue.id}}-{{.event}}`): that is the reliable way to dedup.

In a template `.field.sub` reads the body's own fields (including any named `headers`, `raw` or `body`). What isn't the body is under `.seaport`: `.seaport.headers` has the request's content type, user agent and `X-` headers (never the ones carrying credentials) and `.seaport.raw` is the body as text; a body field named `seaport` is hidden by it. A header name with a dash needs `index`: `{{index .seaport.headers "x-event"}}`. The functions are `json`, `join`, `lower`, `trunc` and `default`, plus Go's `index`, `len`, `eq`, `ne`, `lt`, `le`, `gt`, `ge`, `and`, `or` and `not`, for example `{{join ", " .tags}}`, `{{trunc 80 .issue.title}}`, `{{.user | default "unknown"}}`. A missing key renders empty.

Templates are kept cheap on purpose. `printf`, `print`, `println`, `call`, `html`, `js`, `urlquery`, `slice` and `{{template}}` are refused; `{{range}}` only goes over a field or variable (`{{range .items}}`), at most 3 per field and never nested. A field may write 64 KiB and run 200 ms; past that it fails and its default is used.

```json
{
  "chat_id": "{{.issue.id}}",
  "chat_title": "{{.issue.title}}",
  "external_id": "{{.issue.id}}-{{.event}}",
  "text": "[{{.level}}] {{.issue.title}}",
  "tags": "sentry, {{.issue.level}}",
  "agent": "qa"
}
```

A template is checked when you save it (a syntax error or a refused construct is a `400` naming the field). A field that fails when a body arrives uses its default, so a message is never dropped for its template (the port logs a warning, at most once a minute per dock). Values are cleaned of control characters and cut to the usual limits.

Save it, with a preview of how a sample body would look, in the dock's page (**Plantilla**, **Guardar y probar**) or:

```sh
seaport dock webhook <dock-id> -template template.json -sample body.json
```

Show the URL and the stored template with `seaport dock webhook <dock-id>`.

## What agents see

Every webhook conversation is a group where the message is always addressed, so agents wake without a mention. The sender is `sender_id` and also carries the handle `webhook:<first part of the dock id>`, so a customs rule can allow or deny the dock as a whole. The conversation is read-only: the prompt says so and sends the agent to a linked door.

## Show it where people talk

A webhook dock can't take the agent's answer, so give it a [mirror](mirrors.md): Seaport posts each new conversation at a Slack channel (or another destination) and the agent and people talk there. For CloudWatch alarms see [CloudWatch](cloudwatch.md).

## Close it

`seaport dock unlink <dock-id>` (or **Cerrar el webhook**) deletes the secret and the template: the URL answers `404`. History stays. `seaport dock relink <dock-id>` opens it again with a new secret to generate.

## Limits

See [Limits](../reference/limits.md#webhook-docks): 1 MiB per body, 20 events per call, 60 calls a minute per dock. The HTTP routes are in the [HTTP API](../reference/http-api.md#webhook-docks).
