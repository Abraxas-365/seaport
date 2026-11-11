# Answer issues on Jira

A `jira` dock is a service account on your Jira Cloud site: mention one of your agents (or the account) in an issue comment and it answers there with a comment.

Each issue is one conversation (`PROJ-123`). Customs decides who may wake an agent, the dock's team decides which agent answers, and the agent's `reply` becomes a comment on the issue. Use it for triage, support follow-ups or QA notes, with the agents you already run on WhatsApp, GitHub or the `api` dock.

> [!NOTE]
> Jira must reach your port: the dock needs a public `https` `SEAPORT_PUBLIC_URL` (see [Deploy](operations/deploy.md)) and the [secret key](../reference/configuration.md#secret-key), which seals the API token and the webhook secret. For a port on your laptop, put a tunnel in front of it and set `SEAPORT_PUBLIC_URL` to the tunnel's address. Only Jira Cloud sites are supported (`*.atlassian.net` by default, see [Jira configuration](../reference/configuration.md#jira)).

## Create the service account and its token

1. In your Atlassian organization, create the account agents will act as, for example `Seaport Bot` (`bot@example.com`), and give it access to the projects it should read and comment on (the *Browse projects* and *Add comments* permissions).
2. Sign in as that account and create an API token at `https://id.atlassian.com/manage-profile/security/api-tokens`. Copy it: Atlassian shows it once.

## Create the dock

1. Create the dock: **Muelles → Nuevo muelle → Jira** in the web app, or `seaport dock add jira -name support`, which prints the dock's page.
2. On the dock's page fill in the site (`https://example.atlassian.net`), the account's email and the API token, then **Conectar**. Seaport calls `GET /rest/api/3/myself` with them before storing anything, so a wrong token or an account that can't use the site is refused.
3. The dock is **linked**, and its address is the site's host. The page now shows the **webhook URL** and the **webhook secret**. Copy the secret now: Seaport stores it sealed and never shows it again (**Nuevo secreto** makes another one).

Linking again with a new token (**Cambiar credenciales**) keeps the webhook secret, so Jira's side doesn't change. Through the API: `PUT /api/v1/docks/{id}/jira` (see [HTTP API](../reference/http-api.md#jira-docks)).

## Send Jira's events to Seaport

Pick one of the two ways; both prove themselves with the dock's webhook secret.

**An admin webhook** (Jira settings → System → WebHooks, which needs Jira administrator rights):

1. URL: the dock's webhook URL (`https://port.example.com/jira/webhook/<dock-id>`).
2. Secret: the dock's webhook secret. Jira signs each body with it and sends `X-Hub-Signature: sha256=<hex HMAC>`; Seaport checks it in constant time.
3. Events: **Comment → created** and **Issue → created**. Add a JQL filter (for example `project = PROJ`) so Jira only sends what the agents should see.

**A Jira Automation rule**, when you aren't an administrator: trigger *Comment added* (and/or *Issue created*), action **Send web request** with:

- URL: the dock's webhook URL, method `POST`;
- header `Authorization: Bearer <webhook secret>`;
- header `Content-Type: application/json`;
- body *Automation format* (or custom data) that includes `webhookEvent` (`comment_created` or `jira:issue_created`), `issue` (`id`, `key`, `fields.summary`, `fields.description`) and for comments `comment` (`id`, `body`, `author`). The default "Issue and comment" webhook body of Automation already has that shape.

## What agents see

Seaport forwards a comment only when it **mentions the account** (a mention picked in Jira, or `@<display name>` as plain text when the name is one word) or **an agent of the dock's team** (`@qa`). Everything else is ignored, so agents never read a project's whole traffic. Comments by other apps and Automation (`accountType: app`) are ignored too, which stops loops.

- A new issue (`jira:issue_created`) arrives as one message: summary, then description, under the same rule.
- The conversation is the issue key (`PROJ-123`), titled `PROJ-123: <summary>`. Customs rules can match the key or its project (`PROJ`), and senders by Atlassian account id or email.
- Text is normalized to Markdown whatever Jira sent: ADF (comments in the REST format) and wiki markup (admin webhooks) become headings, lists, tables, code blocks with language, quotes and links.
- The agent's `reply` is written in Markdown and posted as a comment in Atlassian Document Format: paragraphs, headings, bold/italic/strikethrough, inline code, code blocks with language, nested lists, links, quotes, rules and simple tables. Long replies are split into several comments.
- Comments posted by the service account itself are recorded as sent and wake nobody. Seaport stamps its own comments with the comment property `seaport` (its message id), so the webhook of a reply is recognized as the stored message, not stored twice.

## Limits

- Jira docks don't send files yet: a reply with files is refused (`422`). Link to files with full URLs instead.
- Replies are comments only: transitions, assignments and field edits aren't part of the dock.
- A comment's text is capped at 24 000 characters of Markdown; longer replies become several comments.

## Troubleshooting

| Symptom | Likely cause |
| :--- | :--- |
| Linking says Jira refused the credentials | Wrong email or token, a token created by another account, or the account has no access to the site. Check with `curl -u bot@example.com:<token> https://example.atlassian.net/rest/api/3/myself`. |
| Linking says the site isn't allowed | The host isn't in `SEAPORT_JIRA_HOSTS` (default `*.atlassian.net`). |
| The page warns that Jira can't reach the address | `SEAPORT_PUBLIC_URL` is plain `http` or loopback. |
| Webhook deliveries answer `401` | The secret differs from the dock's (it was rotated?) or the body was altered by a proxy. |
| Deliveries answer `202 ignored` | The comment mentions neither the account nor a team agent, the author is an app, or the event isn't `comment_created` / `jira:issue_created`. |
| Nothing reaches the agent | Customs holds it (check **Aduana**), or the dock has no team (see [Agents and routing](agents-and-routing.md)). |
| The dock turns **logged out** | Jira answered `401`/`403` to the API token (expired, revoked, account deactivated). Link the dock again with a new token. |

**Desconectar Jira** (or `POST /api/v1/docks/{id}/unlink`) deletes the stored site, token and secret. Nothing is revoked in Jira: revoke the token and remove the webhook or the Automation rule there.
