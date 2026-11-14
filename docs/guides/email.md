# Answer email

An `email` dock is a mailbox: Seaport reads its folder over IMAP, and an agent's reply goes out over SMTP in the same thread.

Each email thread is one conversation. Customs decides who may wake an agent, the dock's team decides which agent answers, and the agent's `reply` becomes an email to the thread's participants with `Re:` and the threading headers, so it lands in the same thread in their mail client. Use it for a support address or a shared inbox, with the agents you already run on other channels.

> [!NOTE]
> The dock needs the [secret key](../reference/configuration.md#secret-key), which seals the mailbox password. It needs no public URL: Seaport connects out to your mail servers, so it works from a laptop. Only encrypted connections are accepted (TLS or STARTTLS). Gmail, Outlook and iCloud need an app password, not the account's own.

## Connect a mailbox

1. Create the dock: **Muelles → Nuevo muelle → Correo** in the web app, or `seaport dock add email -name support`.
2. On the dock's page pick the provider (Gmail, Outlook / Microsoft 365, iCloud, or **Otro** to name the servers), type the address and the password, then **Conectar**. From the CLI:

   ```sh
   seaport dock email <dock-id> -address support@example.com -provider gmail
   seaport dock email <dock-id> -address support@example.com -imap mail.example.com -smtp mail.example.com:587 -username support
   ```

   The password is read from the terminal (hidden) or from stdin, never from the command line. `-folder` picks another folder than `INBOX`; `-display-name` is the name on replies. A server given as `host` uses implicit TLS (993 for IMAP, 465 for SMTP); ports 143 and 587 use STARTTLS.
3. Seaport logs in to both servers and opens the folder **before storing anything**, so a wrong password, a closed port or an unknown folder is refused (`422`) and nothing is saved. Then the dock is **linked** and its address is the mailbox address.

Mail that was already in the folder is not read: a new dock starts at the end of the folder, so linking a busy inbox doesn't wake agents with its history. Only messages that arrive afterwards become ships.

To change the password or servers, use **Cambiar credenciales** (or the same command again). It keeps the position in the folder, so nothing is read twice or skipped. Through the API: `PUT /api/v1/docks/{id}/email` (see [HTTP API](../reference/http-api.md#email-docks)).

### Provider notes

| Provider | IMAP / SMTP | Password |
| :--- | :--- | :--- |
| Gmail | `imap.gmail.com` / `smtp.gmail.com` | An app password (2-step verification on) and IMAP enabled in Gmail's settings. |
| Outlook / Microsoft 365 | `outlook.office365.com` / `smtp.office365.com` | Basic authentication must be allowed for the mailbox; many tenants require an app password or have it off. |
| iCloud | `imap.mail.me.com` / `smtp.mail.me.com` | An app-specific password from the Apple account. |

OAuth sign-in is not supported, only passwords.

## What agents see

- The first message of a thread starts with `Subject: <subject>`, then the text. Later messages are just the text, with the quoted history (`>` lines, "On … wrote:", "-----Original Message-----", forwarded header blocks) removed so the agent doesn't read the whole thread again.
- A thread is found by `In-Reply-To` and `References`; a reply from a client that dropped those headers joins the thread with the same subject (ignoring `Re:` and `Fwd:`) within 14 days. Either way the **sender must already be in the thread**: a message that names someone else's `Message-ID` starts a thread of its own, so customs decides about it and it can't add its sender to a conversation that isn't theirs. Anything else starts a new conversation, titled with the subject, keyed by the message's `Message-ID`.
- HTML-only mail is converted to plain text. Images arrive as images (when media storage is on, see [messages and images](messages-and-images.md)); other attachments are named in the text as a note and not kept.
- The sender is the `From` address (lowercase). The conversation's handles are the sender, the sender's domain (`@example.com`) and the people the thread already writes to (at most 16), so [customs](customs.md#rules) rules match addresses and patterns such as `ana@example.com`, `*@example.com` or `@example.com`. The `To` and `Cc` of a message are never handles: copying someone a rule allows doesn't let a stranger in. A new dock asks first (`default ask`): the first mail from someone waits at customs until you allow it.
- A thread is a **group** when it has more than one other person; a message is **addressed** only when the mailbox is in `To` (not just `Cc` or `Bcc`), so a group in `mentions` mode holds mail that only copies the mailbox.
- Mail Seaport doesn't forward: messages from the mailbox's own address, automatic mail (`Auto-Submitted` other than `no`, `Precedence: bulk|junk|list`, `X-Autoreply`), and delivery reports from `mailer-daemon`/`postmaster`. This stops loops with other autoresponders and with its own replies.
- Mail whose sender isn't authenticated: see [Who wrote it](#who-wrote-it).
- Messages over 30 MiB are not downloaded; only their headers are read and the text says so.

## Who wrote it

`From` is whatever the sender typed. Seaport doesn't check SPF or DKIM itself (that is the job of the mailbox's server); it reads the **`Authentication-Results`** header the server adds when mail arrives (Gmail, Outlook, iCloud and Fastmail all do), and trusts only that header's first line:

- **DMARC pass**, or **DKIM pass** or **SPF pass** for a domain that matches the sender's: the mail goes on.
- A result that failed, or passed only for another domain: the mail is skipped.
- **No header at all**: skipped, because nothing vouches for the sender. Set `SEAPORT_EMAIL_REQUIRE_AUTH=false` ([configuration](../reference/configuration.md#email)) to accept such mail; the text then ends with a note, `[sender not verified: …]`, and customs rules on addresses are only as strong as the sender is honest. Use that only for a mail server that does not add the header and that you trust to refuse spoofed senders.

Skipped mail is logged (`email ignored`) and the position moves past it.

## Who gets the reply

The reply goes to the **sender** (or their `Reply-To` when it is on the same domain as `From`; a `Reply-To` elsewhere is ignored) and, once a message of the thread has got through customs, to the other people on its `To` and `Cc`, at most 10 in all. A message that customs holds or denies adds nobody: a stranger can't make an agent write to addresses of their choosing. If the server refuses some recipients the others still get it; the reply fails only when none is accepted.

## Replies

The agent's `reply` is sent to the thread's recipients (above), with subject `Re: <subject>`, `In-Reply-To` and `References` set to the thread, `Auto-Submitted: auto-generated`, and a Seaport `Message-ID` on the mailbox's domain. Files go as attachments. The message is stored before it is sent, so the copy in the mailbox's Sent folder is not read as a new message.

A reply to a conversation with no stored email thread is refused. If SMTP refuses the password, the dock turns **logged out**.

## Connections and several processes

An email dock is bound to one process, like WhatsApp: the process holding its lease keeps one IMAP connection open, and another takes over within about 30 s if it dies (see [several processes](operations/several-processes.md)). The connection waits with IMAP IDLE when the server offers it (new mail arrives within seconds) and otherwise polls every 60 s. A dropped connection is re-made with a growing pause (1 s up to 5 min), and the page shows the last error while it is down. IDLE is renewed every 20 minutes, since servers drop idle connections after about 30. A change of the folder's `UIDVALIDITY` (the server rebuilt it) restarts reading at the end. A message that fails to be taken in three times is logged and skipped so it can't block the mailbox.

If the server refuses the password (an `AUTHENTICATIONFAILED` or `AUTHORIZATIONFAILED` answer) the dock turns **logged out** and stops retrying; connect it again with the new password. Any other refusal (a throttle, "too many connections", "unavailable") is retried with the pause above.

Seaport only reads: the folder is opened read-only, so messages stay unread for the mailbox's owner, and replies are not copied into a Sent folder (the provider's SMTP server usually does that itself).

## Network safety

A dock names servers, so a tenant could point it at your internal network. Seaport refuses IMAP and SMTP hosts that resolve to loopback, private, link-local or otherwise non-public addresses (checked when connecting, so DNS tricks don't help) and certificates it can't verify. Set `SEAPORT_EMAIL_PRIVATE=true` to allow private addresses, for a mail server on your own network. Addresses that embed an IPv4 one (NAT64 `64:ff9b::/96`, 6to4 `2002::/16`) count as non-public. `SEAPORT_EMAIL_HOSTS` restricts which server names a dock may name at all, e.g. `imap.gmail.com,smtp.gmail.com` (see [configuration](../reference/configuration.md#email)).

## Limits

- Password authentication only (no OAuth); plaintext IMAP/SMTP is never accepted.
- One folder per dock; replies go to the participants of the thread, never to a list you pick.
- Attachments other than images are named in the text, not kept.
- Only the mailbox's own server vouches for senders (see [Who wrote it](#who-wrote-it)); there is no Seaport-side SPF/DKIM check.

## Troubleshooting

| Symptom | Likely cause |
| :--- | :--- |
| Linking says the login was refused | Wrong password, or the provider needs an app password / IMAP enabled / basic authentication allowed. |
| Linking says the server is not allowed | The operator limited server names with `SEAPORT_EMAIL_HOSTS`. |
| Mail arrives in the mailbox but is ignored (log `email ignored`) | No or failed `Authentication-Results` (see [Who wrote it](#who-wrote-it)), automatic mail, or the mailbox's own mail. |
| Linking says the server address is private or local | The host resolves to a non-public address. Set `SEAPORT_EMAIL_PRIVATE=true` if the mail server is on your own network. |
| Linking says the certificate is not trusted | The server uses a self-signed or expired certificate. Seaport doesn't skip verification. |
| Linking says the folder doesn't exist | Folder names are case-sensitive and provider-specific (`INBOX`, `[Gmail]/All Mail`). |
| The page shows "Reconectando" and an error | The IMAP connection dropped; Seaport retries by itself. Check the error text. |
| Mail arrives but nothing reaches the agent | Customs holds it (check **Aduana**), the mail is automatic or from the mailbox itself, or the dock has no team (see [Agents and routing](agents-and-routing.md)). |
| Mail sent before linking isn't read | By design: reading starts at the end of the folder when the dock is linked. |
| The dock turns **logged out** | The mailbox refused the password (IMAP or SMTP). Connect it again. |

**Desconectar el buzón** (or `POST /api/v1/docks/{id}/unlink`) deletes the stored password, the thread records and the position in the folder. Nothing is deleted from the mailbox; revoke the app password at your provider.
