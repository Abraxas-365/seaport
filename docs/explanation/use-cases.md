# Use cases

Follow one software story from the backlog to production with seven agents, and see which docks and features each step uses and how to set it up.

The example is a team that tracks work on a Jira board, keeps code on GitHub, talks in Slack (or Teams) and runs on AWS. At each column of the board an agent reviews, prepares or tests, and a person decides what happens next. Every agent is defined once in the port and works only on the docks it is assigned to.

| Agent | Called on | What it does |
| :--- | :--- | :--- |
| `@stories` | Jira | Reviews new stories before refinement. |
| `@refiner` | Jira | Writes acceptance criteria and functional test cases. |
| `@dev` | Jira, GitHub | Implements small stories and opens the pull request. |
| `@reviewer` | GitHub | Reviews the pull request against the story. |
| `@qa` | GitHub, Jira | Tests the pull request in a browser and leaves screenshots. |
| `@pm` | Slack or Teams | Answers how the project is going. |
| `@ops` | Slack or Teams, alerts | Answers about logs and metrics; attends alarms. |

The names are examples: an agent's name is its `@mention` handle, so pick the ones your team will type.

## The pieces you set up once

- **Docks.** A [Jira dock](../guides/jira.md) (a service account and a webhook), a [GitHub dock](../guides/github.md) (a GitHub App installed on the repositories), a [Slack dock](../guides/slack.md), or an [`api` dock](../guides/api-docks.md) for a Teams bridge, and a [webhook](../guides/webhooks.md) or [Datadog](../guides/datadog.md) dock for alerts.
- **Agents.** `seaport agents add <name> -description "…"`, then `seaport agents assign <name> <dock-id>` on each dock it works. Its instructions, skills, MCP servers and secrets go in its [config](../reference/agent-config.md).
- **Where agents run.** On the port in a container per slot ([hosted agents](../guides/hosting/hosted-agents.md)), or on a team server or laptop with [`seaport connect`](../guides/connector.md). Hosted agents get the repositories cloned, a model gateway that keeps keys out of the container and an egress allowlist.
- **Customs.** Who may wake an agent on each dock: a Jira project, the organization's repositories, a Slack channel. See [Customs](../guides/customs.md).

## 1. Review a story before refinement

The product owner writes `@stories please review this story` on a Jira issue. The agent compares it with the backlog, the decisions the team already made and the code, and comments on the issue: another story asks for the same thing, a past decision contradicts it, it touches the reconciliation module, which has no tests. It ends by suggesting the next step, such as calling `@refiner`.

What it uses:

- The **Jira dock**: a mention of an agent of the dock's team in a comment wakes it, and its `reply` is a comment on the issue.
- A **vault** with the team's decisions, which the agent reads and adds to with the `memory_search` and `memory_save` tools: `seaport vault create decisions`, then `seaport vault grant decisions -agent stories -mode write`. See [Vaults](../guides/vaults.md).
- The **repository** cloned in the agent's slot (`machine.repos` in its config), so it can see which code a story touches. A read-only credential is an agent secret.
- To search the backlog, give the agent a Jira MCP server or API token in its profile. The dock itself forwards only the comments that call an agent, so agents never read a project's whole traffic.

## 2. Write acceptance criteria and test cases

On the same issue, someone calls `@refiner`. It writes the criteria as Given / When / Then and a table of functional cases (id, case, data, expected result). These are the cases `@qa` runs in step 5.

What it uses:

- **Routing by mention**: the first `@name` of a member of the dock's team picks the agent, so `@stories` and `@refiner` share the Jira dock. See [Agents and routing](../guides/agents-and-routing.md).
- **Skills** with the team's format for criteria and cases, loaded by the agent when it needs them. See [Profiles](../guides/profiles.md).
- **Linked conversations**: link the Jira issue and its pull request (`seaport thread link <conversation-id> <conversation-id>`) and the agents that work the pull request read what was said on the issue, in one session. See [Linked conversations](../guides/linked-conversations.md).

## 3. Development: a person or an agent

The story is assigned as usual. A developer takes it with their own tools and can still ask the agents on the issue. For a small, well-defined story, someone can ask `@dev` for a first pull request instead: a hosted agent works in its own branch (one git worktree per conversation) and opens the pull request as the App's bot, never as a person. That pull request goes through the same review as any other.

What it uses: [hosted agents](../guides/hosting/hosted-agents.md), the GitHub App's token for `gh` and git ([Act on GitHub as the App](../guides/github.md#act-on-github-as-the-app)), and linked conversations.

## 4. Review the pull request

Someone writes `@reviewer please review` on the pull request, or a label runs a GitHub Actions workflow that hands the pull request to the agent. The agent leaves a formal review (approve or request changes) with comments on the lines of the diff, checked against the story's criteria: "Criterion 2 isn't met: if the second charge fails, the first stays captured."

What it uses:

- The **GitHub dock**: each pull request is a conversation, and `reply` is a comment. Formal reviews and line comments go through `gh` with the App's token.
- The **GitHub Action**, which needs no secret in the repository: GitHub's OIDC token proves the run to the port. See [Trigger an agent from a workflow](../guides/github.md#trigger-an-agent-from-a-workflow).
- **Customs**: `seaport customs -dock <dock-id> allow "acme/*"` lets the organization's repositories in; `deny github:action` turns workflows off.
- A complete reviewer to start from: [`examples/github-reviewer`](../../examples/github-reviewer/README.md).

## 5. Functional QA in a real browser

`@qa` runs the story's cases on staging in a Chromium that runs beside the agent and posts a report on the pull request: a table of cases with pass or fail, and the screenshots. When the application asks for a login or a 2FA code, a person does it from the web app's live viewer; the password never goes through the agent, and the agent keeps the session.

What it uses:

- The **managed browser**: `browser: {engine: chromium}` in the machine, and `seaport agents profile set qa -browser on`. With `-browser-assist conversation` Seaport tells people in the conversation when the agent needs a takeover. See [Browser](../guides/hosting/browser.md).
- **Files in replies**: the agent attaches the report and the screenshots to its `reply` (or `seaport reply -file`). On GitHub they are committed to a `seaport-files` branch and shown in the comment. See [Files agents send](../guides/messages-and-images.md#files-agents-send).
- An **egress allowlist** with the staging host: `network.egress` in the machine. See [Model gateway and egress](../guides/hosting/network.md).

## 6. Project and production questions in chat

Managers and leads ask in Slack, or in Teams, without opening Jira, GitHub or the AWS console. "@pm how is sprint 14 going?" gets the closed stories, the risks and what failed in QA. "@ops how is the payments CPU? Any errors in the last hour?" gets the numbers from the metrics and logs. Every morning `@pm` posts a summary on its own.

What it uses:

- A **Slack dock**, or an **`api` dock** for Teams: Seaport has no Teams dock, so a bot or a Power Automate flow posts each message to the `api` dock and reads the answers from the history or the event stream. See [api docks](../guides/api-docks.md).
- A **schedule** for the morning summary: `seaport schedules add <conversation-id> morning -cron "0 9 * * 1-5" -timezone America/Lima -text "Post today's sprint summary"`. See [Schedules](../guides/schedules.md).
- **Agent secrets** for read-only cloud credentials or an MCP server's key: `seaport agents secrets set ops AWS_ACCESS_KEY_ID -env`. See [Variables and secrets](../guides/hosting/secrets.md).

## 7. An alarm wakes the on-call agent

At 3:12 a.m. a CloudWatch alarm fires and nobody is connected. An EventBridge rule posts it to a webhook dock, which wakes `@ops`. The agent investigates and posts in the on-call channel: 4.2 % of 502 errors since 3:12; probable cause, the 3:05 deploy exhausted the database connections. It then asks "What do I do? 1. Roll back 2. Page the team 3. Nothing". The person on call answers `1`; Seaport records who answered and the agent continues.

What it uses:

- A **webhook dock**: any system that POSTs JSON wakes an agent, with a template that shapes the message and dedups retries. A Datadog monitor goes to a **Datadog dock** instead, which can mirror each alert to a Slack channel or WhatsApp group. Both only trigger: the agent answers on another dock. See [Webhooks](../guides/webhooks.md) and [Datadog](../guides/datadog.md).
- A **destination**: the owner names the on-call channel (`seaport dock destination set <slack-dock-id> oncall <channel-id> -title "On-call"`), and the agent opens a conversation there with the `open` tool. An agent can't post anywhere else it wasn't addressed. See [Destinations](../guides/destinations.md).
- An **approval**: the `ask_approval` tool sends the numbered options and waits; people answer on the channel or in the web app. See [Approvals](../guides/approvals.md).

## Control

Each step above runs under the same rules, set by whoever runs the port:

| Rule | Where |
| :--- | :--- |
| Who may wake an agent, per channel, project, repository or person; agents can't change it | [Customs](../guides/customs.md) |
| Secrets sealed at rest; a hosted slot reaches its model through the gateway, so the key never enters the container | [Variables and secrets](../guides/hosting/secrets.md), [Model gateway and egress](../guides/hosting/network.md) |
| Each hosted slot reaches only the hosts on its list and its repositories | [Model gateway and egress](../guides/hosting/network.md) |
| Model usage per agent, model and day | [Model gateway and egress](../guides/hosting/network.md#usage) |
| Each agent is one versioned document | [Agent config](../reference/agent-config.md) |
| What agents may read on each dock: history, other conversations, files | [Agents and routing](../guides/agents-and-routing.md#what-agents-may-read) |
| Before a consequential action the agent asks; logins and 2FA stay with people | [Approvals](../guides/approvals.md), [Browser](../guides/hosting/browser.md#when-a-person-must-take-over) |

## Adopt it in stages

Each stage adds agents without changing the ones before:

1. **Review**: `@stories` and `@reviewer`. Needs the Jira and GitHub docks.
2. **Functional QA**: `@refiner` and `@qa`. Adds a hosted agent with a browser.
3. **Operations**: `@pm` and `@ops`. Adds a chat dock, a webhook or Datadog dock, a destination and approvals.

To run a port for this, see [Deploy on one server](../guides/operations/deploy.md); for the first agent, [Your first agent](../tutorials/first-agent.md).
