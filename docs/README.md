# Seaport documentation

Seaport keeps your company's AI agents in one place and lets people call them from Slack, GitHub, Jira, WhatsApp or your own systems. Customs decides who may wake them.

These pages describe the current `main` branch, which tracks the latest [release](https://github.com/Abraxas-365/seaport/releases). Seaport is early-stage software: formats and APIs may still change. Read the [known limitations](project/known-limitations.md) before relying on it.

![The Seaport inbox, with an agent answering a customer](assets/screenshots/inbox.png)

## Choose a path

| Goal | Start here |
| :--- | :--- |
| Install the binary | [Install Seaport](guides/installation.md) |
| Run a port on your computer and see a message arrive | [Your first port](tutorials/first-port.md) |
| Connect an agent and get a reply | [Your first agent](tutorials/first-agent.md) |
| Find your way around the web app (in Spanish) | [Web app tour](guides/web-app.md) |
| Understand docks, ships, customs and crew | [Concepts](explanation/concepts.md) |
| Call agents with `@name` in Slack channels, threads and DMs | [Slack docks](guides/slack.md) |
| Have agents answer on pull requests and issues | [GitHub docks](guides/github.md) |
| Hand work to an agent from a GitHub workflow | [GitHub Actions](guides/github.md#trigger-an-agent-from-a-workflow) |
| Have agents answer Jira issues and comments | [Jira docks](guides/jira.md) |
| Put an agent behind a WhatsApp number | [WhatsApp docks](guides/whatsapp.md) |
| Feed messages from your own systems over HTTP | [api docks](guides/api-docks.md) |
| Copy a complete agent: a pull request reviewer | [`examples/github-reviewer`](../examples/github-reviewer/README.md) |
| Decide who may wake an agent | [Customs](guides/customs.md) |
| Send and receive images and files | [Messages and images](guides/messages-and-images.md) |
| Follow a message from arrival to reply | [The life of a message](explanation/message-lifecycle.md) |
| Run several agents and pick one with `@name` | [Agents and routing](guides/agents-and-routing.md) |
| Give an agent instructions, skills, MCP servers and secrets | [Agent profiles](guides/profiles.md) |
| Connect rness, Claude Code or any ACP agent | [The connector](guides/connector.md) |
| Let the port run the agent in a container | [Hosted agents](guides/hosting/hosted-agents.md) |
| Keep model keys out of containers and limit their network | [Model gateway and egress](guides/hosting/network.md) |
| Ship your own `~/.rness` or `~/.claude` to hosted agents | [Harness homes](guides/hosting/harness-homes.md) |
| Run hosted Claude Code on a Claude subscription | [Claude subscriptions](guides/hosting/claude-subscriptions.md) |
| Deploy on a server with HTTPS | [Deploy on one server](guides/operations/deploy.md) |
| Serve many organizations | [IAM mode](guides/operations/iam-mode.md) |
| Run several Seaport processes | [Several processes](guides/operations/several-processes.md) |
| Back up, restore and upgrade | [Backups and upgrades](guides/operations/backups-and-upgrades.md) |
| Look up a setting, command, route or limit | [Configuration](reference/configuration.md), [CLI](reference/cli.md), [HTTP API](reference/http-api.md), [Limits](reference/limits.md) |
| Build on the port: tools, events, wire protocol, formats | [MCP tools](reference/mcp-tools.md), [Events](reference/events.md), [Crew protocol](reference/crew-protocol.md), [Agent profile](reference/agent-profile.md), [Environment spec](reference/environment-spec.md), [Permissions](reference/permissions.md) |
| Know what is protected and how | [Security model](explanation/security-model.md) |
| Contribute | [Contributing](contributing/README.md), [Architecture](architecture.md) |

## How these docs are organized

- **Tutorials** walk you through a complete task with a concrete outcome.
- **Guides** show how to do one thing: connect Slack or GitHub, write customs rules, host an agent.
- **Reference** lists every setting, command, route, event, field and limit.
- **Explanation** describes the concepts, the life of a message and the security model.
- **Project** and **Contributing** cover current limitations, the architecture and how to work on Seaport.

## A word on names

Seaport uses a port's vocabulary. The **port** is the server (`seaport serve`). A **dock** is a channel account where messages arrive: a Slack app, a GitHub App, a Jira service account, a WhatsApp number or an `api` dock. A **ship** is a message. **Customs** decides which conversations may wake an agent. The **crew** is the set of connected agents, each run by a **harness** (rness, Claude Code) under `seaport connect` or in a hosted **slot**. See [Concepts](explanation/concepts.md).

The web app is in Spanish. These docs name each page in Spanish with the English meaning in parentheses: **Bandeja** (Inbox), **Aduana** (Customs), **Muelles** (Docks), **Agentes** (Agents), **Entornos** (Environments), **Tripulación** (Crew).

## Documentation scope

Examples use fictional numbers (`+34600000000`…), names and domains (`example.com`). Never put real phone numbers, tokens or keys in documentation, examples or issue reports: the repository is public. Engineering invariants live in [AGENTS.md](../AGENTS.md).
