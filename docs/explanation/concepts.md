# Concepts

Seaport sits between the places people write to you (WhatsApp, your own systems) and the coding agents that answer. This page names the parts and how they fit, so the rest of the docs read easily.

## The picture

```text
 people ──▶ dock ──▶ customs ──▶ routing ──▶ queue ──▶ crew connection ──▶ harness (agent)
 (chats)   (channel)  (may it     (which     (ships)   (seaport connect)     │
                       wake an    agent?)                                    │ replies via MCP
                       agent?)                                               ▼
 people ◀── dock ◀────────────────────────── port ◀────────────────────── reply
```

Everything left of the connector runs in **the port** (`seaport serve`). The connector and the harness run wherever you choose: your laptop, a server, or a container the port starts for you.

## Port

The **port** is the Seaport server: one process (or several behind the same database) that holds docks, conversations, customs rules, agents and the queue, and serves the [web app](../guides/web-app.md), the HTTP API, the live event stream and the crew WebSocket. It stores everything in Postgres.

## Docks

A **dock** is one channel account: a WhatsApp number you linked, a GitHub App you installed on your repositories, a Jira service account that answers issue comments, or an `api` dock that your systems post to over HTTP. Docks are where **ships** arrive and leave. Each dock has its own customs, its own team of agents, and its own send limits. See [WhatsApp](../guides/whatsapp.md), [GitHub](../guides/github.md), [Jira](../guides/jira.md) and [API docks](../guides/api-docks.md).

## Conversations and ships

A **conversation** is a chat on a dock: one person, or a group. It has a status (allowed, pending or denied) that customs decides.

A **ship** is a message. Inbound ships come from people; outbound ships are replies. A ship is text plus optional image attachments. Every ship is stored, whether or not an agent ever sees it. See [Messages and images](../guides/messages-and-images.md).

## Customs

**Customs** decides which chats may wake an agent. Anyone can write to your number; customs keeps strangers and noisy groups away from your agents while every message is still stored and visible to you. It has a default policy, rules and per-chat overrides. A ship that doesn't pass is **held**, never dropped. See [Customs](../guides/customs.md).

## Agents

An **agent** is a named worker in your organization's library (`pm`, `support`). Its name is its `@mention`. An agent works on the docks it is assigned to (its dock's **team**), and a **profile** says how it behaves: instructions, skills, MCP servers, secrets. When a ship is allowed in, **routing** picks one agent of the dock's team for it. See [Agents and routing](../guides/agents-and-routing.md) and [Agent profiles](../guides/profiles.md).

## Crew, connectors and harnesses

An agent in the library is only a name and a profile. The thing that does the work is a **harness**: a coding-agent program such as rness or Claude Code. A **connector** (`seaport connect`) starts a harness, connects to the port over a WebSocket and turns each batch of ships into a prompt. The harness answers through the port's MCP tools (`reply`, `history`…).

The **crew** is the set of live connections. An agent can have several (one per machine), and each connects with a **crew token** that belongs to that agent. If an agent has no connection, its ships wait. See [The connector](../guides/connector.md).

## Environments and slots

If you don't want to run harnesses yourself, the port can run them: an **environment** describes where a hosted agent runs (harness, image, tools, repositories, network, resources), and each hosted agent gets **slots**, the containers that run its connector. The port starts a slot when ships wait and stops it when it idles. Hosting is optional and needs Docker. See [Hosted agents](../guides/hosting/hosted-agents.md).

## Organizations and modes

Everything belongs to an **organization**. The port runs in one of three **modes**, set with `SEAPORT_AUTH`:

| Mode | Who signs in | Organizations |
| :--- | :--- | :--- |
| `none` | nobody; loopback only, for trying it on your computer | one |
| `token` | an admin token (`SEAPORT_ADMIN_TOKEN`) | one |
| `iam` | people sign in with IAMKit and get roles (Admin, Operator, Viewer) | many, fully separated |

In every mode, agents connect with crew tokens, except in `none` mode, where the connector names its agent. See [Operations: IAM mode](../guides/operations/iam-mode.md) and the [security model](security-model.md).

## Where to go next

- Follow a message from arrival to answer: [Message lifecycle](message-lifecycle.md).
- Try it: [Your first port](../tutorials/first-port.md) and [Your first agent](../tutorials/first-agent.md).
- How the code is organized: [Architecture](../architecture.md).
