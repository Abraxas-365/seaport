# Give hosted agents a browser

Run a managed Chromium beside each hosted agent, sign in to sites for it from the web app, and let it keep those sessions.

Many of an agent's jobs end in a browser: a dashboard behind a login, an internal tool with no API, a form to fill. An agent can drive a browser on its own, but it can't sign in for you: passwords and 2FA prompts are a person's job, and they should never be in a prompt. Seaport splits the work: the slot runs a browser whose profile (cookies, storage) persists in its state volume; you open that same browser from the web app, sign in, and close it; the agent then uses the session with its browser tools.

## Turn it on

Two switches, one per side:

1. **The environment has a browser** (capacity). Add it to the spec:

   ```yaml
   browser:
     engine: chromium
   network:
     egress:
       - app.example.com
       - "*.example.com"
   ```

   The next build of the environment installs Chromium, Xvfb (a virtual display), Node and the browser MCP server ([chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp)) in the image. The sites the browser visits go through the egress proxy like everything else, so list them in `network.egress`.

2. **The agent uses it** (usage). On the agent's page, under **Perfil**, turn on **Navegador**, or:

   ```sh
   seaport profile set nav -browser on
   ```

With both on, every slot of the agent starts Chromium at start (headed on a virtual display, CDP on loopback, profile in the state volume under `browser/`), and each session of the harness gets the MCP server `browser` with the DevTools tools: `navigate_page`, `click`, `fill`, `take_snapshot`, `take_screenshot` and the rest. The Seaport MCP adds `ask_takeover`.

Chromium runs headed, not headless, because sites refuse a headless browser far more often: Google's sign-in refuses a headless Chromium at the first step and lets a headed one through. An image built by an older Seaport has no Xvfb; its slots run Chromium headless and log a warning until you rebuild the environment (`seaport env build <env> -force`, then restart the slots).

An agent with the switch on but hosted on an environment without a browser (or run by `seaport connect` on someone's machine) gets no browser; the connector logs a warning and goes on.

## When a person must take over

When a site asks the agent for something only a person may do — credentials, a captcha, a 2FA code, a "verify you are human" wall, a payment — it calls `ask_takeover` (or Seaport [detects the page](#seaport-detects-it) and asks for it) with the site's host, what the person must do (`login`, `captcha`, `2fa` or `other`, with a note) and the conversation it is working, tells the person in the conversation, and goes on with what doesn't need it. The takeover shows up on the agent's page, in the **Navegador** card: *El agente necesita que inicies sesión en app.example.com* (or *resuelvas un captcha*, *introduzcas el código 2FA*, *hagas algo*). **Abrir** opens the viewer on that host; **Listo** ends it. The agent keeps at most 8 open takeovers, one per host; the newest replaces an older one for the same host.

### Telling a person, and resuming the agent

Left to itself, the agent tells the person in the conversation. The profile's `browser_assist` makes Seaport do it, with a link to the agent's browser (`SEAPORT_PUBLIC_URL`; without it the message says where in the web app):

- `conversation`: a message in the conversation the agent named in the call — *El agente **surf** necesita que una persona inicie sesión en app.example.com. … Abre su navegador: https://seaport.example.com/agents/…#navegador. Cuando termines, pulsa Listo.* The conversation must be allowed and the dock must take replies, as for any reply.
- A [destination](../destinations.md) name (`oncall`, `it-support`): Seaport opens a conversation at that destination of one of the agent's docks (a Slack thread, a WhatsApp chat) with the same message; when the destination's dock is the conversation's own, the new conversation joins its thread.
- Empty (default): nobody; the takeover shows only in the web.

In the web, a takeover shows on the agent's **Navegador** card and, when the agent named a conversation, as a banner in that conversation in the **Bandeja**, with **Abrir navegador** and **Listo** (see [the conversation panel](../web-app.md#the-conversation-panel)).

One notice per host: asking again for the same host updates the takeover and sends nothing. When the person presses **Listo** (or `DELETE /api/v1/agents/{id}/takeovers/{host}`), the agent gets a system message (`system:browser`) in the conversation the notice went to — else the one the call named — addressed to it, so it wakes up and goes on in the browser; a takeover nobody was told about in any conversation wakes nobody (the agent finds out when it retries). The agent can also wait in the call (`wait_seconds`, up to 290 s) when the step is short: a code, a captcha.

A takeover nobody attends within an hour is swept (every 30 s, with the other sweepers): it leaves the **Navegador** card and the agent gets a system message saying nobody came, in the same place, so it goes on without that step or asks again when it matters.

```sh
seaport agents profile set surf -browser on -browser-assist conversation
seaport agents profile set surf -browser-assist oncall     # a destination of one of its docks
seaport agents profile set surf -browser-assist none
```

A takeover is also visible in the API: `GET /api/v1/agents/{id}` answers `takeovers` (`host`, `kind`, `note`, `by`, `path`, `conversation_id`, `at`, `notified_at`, `notified_in`), and `DELETE /api/v1/agents/{id}/takeovers/{host}` ends one. Both change `agent.updated`, and each ask, **Listo** and expiry is an `agent.takeover` [event](../../reference/events.md): who asked (the agent or the detector), for what, and who ended it — the audit trail of people entering the agent's browser, kept in the event log after the card clears.

### Seaport detects it

A model doesn't always notice it is on a sign-in page, and one that does may try to get past it. The profile's `browser_detect` (on by default when the web turns the browser on; `seaport agents profile set surf -browser-detect on`) makes the slot's connector watch the browser itself: it keeps a passive DevTools session on the browser and, every time a page finishes loading (and once more a few seconds later, for widgets that render late), reads the page with a small script (`internal/connector/detect.js`) that says whether it needs a person:

- **login**: a visible password field, or a known sign-in host (Google, Microsoft, Apple, Okta, Auth0, Atlassian, Salesforce, AWS, GitHub's `/login`…).
- **captcha**: a reCAPTCHA, hCaptcha, Turnstile, Arkose, DataDome, PerimeterX, GeeTest or AWS WAF widget or wall.
- **2fa**: a one-time-code input, or a row of single-digit boxes. Ranked first; then login, then captcha.

The script reads the DOM and writes nothing; the page's content never leaves the slot. A hit records the takeover (`POST /crew/takeovers`, `by: detector`, with the page's path and never its query) on the conversation of the newest turn running, tells a person as `browser_assist` says, and steers a note into every running turn: the page needs a person, whether one was told, never guess credentials or codes, go on with what doesn't need it or wait. The **Navegador** card shows *Seaport detectó que el navegador necesita que inicies sesión en sso.example.com/signin*. A host is asked at most once every 10 minutes while the page keeps needing someone; nothing is asked while a person is in the viewer (they are on it), nor for `browser_ignore_hosts` (sites and their subdomains the agent never needs to pass: `-browser-ignore intranet.example.com,wiki.example.com`). Detection is independent of `browser_assist`: with nobody to tell, the takeover still shows in the web and the agent's note says so.

A sign-in that refuses automated browsers is not a takeover. Google may answer *Couldn't sign you in · This browser or app may not be secure* (`accounts.google.com/…/rejected`) to a browser it takes for automated: always to a headless one, and sometimes to a headed one too. Nobody gets past that page, not even a person in the viewer, since the viewer drives the same browser. The detector reports it as refused instead of asking for a person. Running turns get a note, at most once per host every 10 minutes: don't retry, and use the site's own sign-in or its API or MCP server instead, with keys stored as the agent's secrets. The viewer shows the same advice over the page: *Google no deja iniciar sesión en este navegador*. See [Sites that refuse automated browsers](#sites-that-refuse-automated-browsers).

```sh
seaport agents profile set surf -browser on -browser-detect on -browser-assist oncall
seaport agents profile set surf -browser-ignore intranet.example.com
seaport agents profile set surf -browser-detect off
```

## The viewer

**Abrir navegador** in the agent's **Navegador** card opens the slot's browser in the page: a live screencast with an address bar, back, forward, reload, tabs, and your mouse and keyboard. Type the site's address, sign in as you would, and close the viewer. Keys you type in the page go only to the slot's browser, not to the web app's shortcuts. Accents typed with dead keys work, and Cmd/Ctrl+V pastes your own clipboard. Nothing you typed leaves the browser: the password goes from your keyboard to the slot's Chromium over the port's WebSocket; the port relays bytes and keeps none.

- **Sesiones** lists the sites the browser holds cookies of, with their expiry, and forgets one on demand. That's how you sign the agent out of a site.
- While the agent is in a turn, the viewer is read-only (*El agente trabaja*): you see what it does without moving its mouse. **Tomar el control** overrides that.
- The agent knows you are there: when the viewer opens, the slot's connector tells the running turn that a person is using the browser and it should wait (or work on what doesn't need it), and when it closes, that the browser is its own again and it should check the page. A turn that starts while you are in the viewer gets the same note. It is a hint to the model, not a lock: the agent's browser tools keep working.
- One viewer per slot at a time; a second opener gets `409`. A viewer ends after 10 minutes without a message or 2 hours in all.
- An open viewer keeps the slot busy: `idle_stop` doesn't stop it and a new environment version doesn't restart it while you are in it. The port marks the slot every 20 s; a mark older than a minute (the port that relayed the viewer went away) no longer counts.
- The viewer needs the slot running with the browser on (both switches). A stopped slot starts when work arrives, or start it from the **Alojamiento** section.
- **Security keys and passkeys don't work in the slot's browser**: no USB key or device authenticator can reach a container. So the browser has no WebAuthn authenticator and never opens Chromium's *Use your security key* dialog, which the viewer can't show and which would block every click on the page. A site that asks for a key (Google's 2-Step Verification, Okta, GitHub…) gets no answer and offers its other methods: click **Try another way** (or the site's equivalent) and pick a code, a phone prompt or a backup code.

Opening and closing a viewer publish `slot.browser` (`opened`/`closed`, with who did it); the stream itself is never stored. The permission is `seaport:browser:use` (Admin and Operator in iam mode); see [Permissions](../../reference/permissions.md).

## What persists and where

The browser's profile is in the slot's state volume, with the harness's sessions. It survives restarts and new versions of the environment, and goes away with the slot. Each slot has its own profile: an agent with two slots has two browsers, and a session you sign in on `nav-1` isn't on `nav-2`. Most agents need one slot; with more, sign in on each.

The egress proxy sees the browser's hosts like any other traffic: a site the environment doesn't allow shows up in **Salidas bloqueadas** on the agent's page. Chromium's own background hosts (`accounts.google.com`, `update.googleapis.com`, …) are refused and harmless.

## Limits

- Chromium only, headed on a 1280×944 virtual screen (pages get about 1280×800). No extensions, no downloads to your machine.
- The viewer streams JPEG frames over a WebSocket; on a slow link it lags but stays usable for a sign-in.
- Local agents (`seaport connect`) get no browser yet.
- Some identity providers refuse automated browsers outright; see below.

## Sites that refuse automated browsers

The agent's Chromium is driven over the DevTools protocol, and some sign-ins refuse a browser they take for automated. The best-known case is *Sign in with Google*, which answers *This browser or app may not be secure*. Running Chromium headed avoids the most common trigger, but Google can still refuse it. Seaport doesn't disguise the browser any further: that goes against those providers' terms, they detect it and break it often, and the account at stake is yours. When a sign-in refuses it, use one of these instead:

- **The service's API or MCP server.** For tools the agent consults (monitoring, trackers, CRMs), an API key is steadier than a browser session and never expires mid-task. Store the keys as the agent's secrets and add the server to its profile, with headers that name them. For example, Datadog's MCP server takes `DD_API_KEY` and `DD_APPLICATION_KEY` headers:

  ```json
  "mcp_servers": [
    {
      "name": "datadog",
      "url": "https://mcp.datadoghq.com/v1/mcp",
      "headers": [
        {"name": "DD_API_KEY", "value": "${DD_API_KEY}"},
        {"name": "DD_APPLICATION_KEY", "value": "${DD_APPLICATION_KEY}"}
      ]
    }
  ]
  ```

  Prefer keys of a service account with read-only scopes. The environment's egress must allow the server's host.
- **The site's own sign-in.** An email-and-password account of the site itself (not *Sign in with Google*) works in the viewer like any other sign-in, 2FA included.

## Related

- [Hosted agents](hosted-agents.md): environments, slots and the state volume.
- [Network](network.md): the egress proxy the browser goes through.
- [Environment spec](../../reference/environment-spec.md#browser) and [Agent profile](../../reference/agent-profile.md): the two switches.
- [MCP tools](../../reference/mcp-tools.md#ask_takeover): `ask_takeover`.
