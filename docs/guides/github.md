# Answer issues and pull requests on GitHub

A `github` dock is a GitHub App of your own: mention one of your agents in an issue or pull request and it answers there with a comment.

Each issue or pull request is one conversation. Customs decides who may wake an agent, the dock's team decides which agent answers, and the agent's `reply` becomes a comment on that thread. Use it for code review, functional QA of a pull request, or docs work, with the agents you already run on WhatsApp or the `api` dock.

> [!NOTE]
> GitHub must reach your port: the dock needs a public `https` `SEAPORT_PUBLIC_URL` (see [Deploy](operations/deploy.md)) and the [secret key](../reference/configuration.md#secret-key), which seals the App's private key and webhook secret. For a port on your laptop, put a tunnel in front of it and set `SEAPORT_PUBLIC_URL` to the tunnel's address.

## Create the dock and its App

1. Create the dock: **Muelles → Nuevo muelle → GitHub** in the web app, or `seaport dock add github -name qa`, which prints the dock's page.
2. On the dock's page, choose the GitHub organization that will own the App (empty: your personal account) and a name, then **Crear GitHub App**. GitHub shows the App it is about to create, already filled in by Seaport: confirm it.
3. GitHub sends you back to Seaport, which keeps the App's credentials, and then to the App's install page. Pick the repositories (or all of them) and install.
4. Back on the dock's page the dock is **linked**, and its address is the App's slug, such as `seaport-qa`.

Install the App on more accounts or repositories at any time from the dock's page (**Instalar en otra cuenta o repositorio**) or from the App's settings on GitHub: Seaport follows installations, suspensions and uninstalls through the App's webhook. When every installation is gone the dock shows **logged out** until you install it again.

The App asks for these permissions:

| Permission | Why |
| :--- | :--- |
| Issues: read and write | read issues, post comments |
| Pull requests: read and write | read pull requests and reviews |
| Contents: read and write | files agents attach to replies (`seaport-files` branch), branches agents push |
| Metadata: read | required by GitHub |

It subscribes to `issues`, `issue_comment`, `pull_request`, `pull_request_review` and `pull_request_review_comment`.

## Let the right people in

A new dock asks first (`default ask`), like any other: the first thread that calls an agent waits under customs. On a private repository where everyone may use your agents, allow the dock by default. On a public one, also deny outsiders, so only members and collaborators wake an agent:

```sh
seaport customs -dock <dock-id> default allow
seaport customs -dock <dock-id> deny github:outsider
```

Each message carries these handles for [customs rules](customs.md):

| Kind | Handles |
| :--- | :--- |
| Thread (chat) | `owner/repo#12`, `owner/repo`, `owner` |
| Author (sender) | `@login`, `github:member` (owner, member or collaborator of the repository) or `github:outsider`, and `association:<value>` with GitHub's `author_association` in lower case (`association:contributor`, `association:first_time_contributor`, …) |

So `seaport customs allow "acme/*"` lets in every repository of `acme`, and `deny @someone` holds that person's messages everywhere.

## Call an agent

Assign agents to the dock as usual ([Agents and routing](agents-and-routing.md)), then mention one by name in:

- a new issue or pull request (title and description),
- a comment on an issue or pull request,
- a review, or a comment on a line of the diff.

```text
@qa please test this: sign in with the test user, create an order, check the total.
```

Seaport forwards a message only when it mentions an agent of the dock's team (`@qa`) or the App itself (`@seaport-qa`); everything else is ignored. Mentioning the App routes like a message that names no agent: to the agent already working that thread, the only agent of the team, or the default one. Comments by other bots are ignored, and the App's own comments never wake anyone, so agents never wake each other in a loop. The App's comments are still recorded in the conversation as sent messages (see [The answer](#the-answer)).

The agent gets the thread as a group conversation titled `PR acme/web#12: <title>` (or `Issue …`); review text starts with the verdict (`(review: changes_requested) …`) and line comments with their place (`(on src/pay.ts:12) …`). Seaport reacts with 👀 on the comment once an agent has it. GitHub's redeliveries are recognized and stored once.

## The answer

The agent answers with the MCP `reply` tool, as on any channel: Seaport posts the text as a comment on the thread, with the App's identity (`seaport-qa[bot]`), within the dock's [send limits](../reference/limits.md). You see the whole thread in the web app's inbox too.

To attach files (a QA report, screenshots), the agent replies with `seaport reply <conversation_id> -file qa-report.md -file checkout.png "QA done"` (or `reply`'s `files`). Seaport commits them in one commit to the repository's `seaport-files` branch, made on first use, under `pr-<number>/<message id>/`. The comment shows the images and links the other files at that commit. The App needs **Contents: read and write** (the manifest asks for it). See [Files agents send](messages-and-images.md#files-agents-send).

Comments the App posts come back to Seaport by webhook. The echo of a reply Seaport sent is matched to the stored message by a hidden marker (`<!-- seaport-message … -->`) and not stored twice. A comment the agent posted itself with `gh` (a review summary, say) is recorded as a sent message of the agent that is working the thread. None of them wakes an agent.

Formal reviews, line comments, labels and pushes are not `reply`'s job: agents do them with `gh` and git, as the App (next section).

## Act on GitHub as the App

An agent of the dock can get a token of the App for a repository the App is installed on, and use `gh` and git with it: a formal review with a verdict, comments on lines of the diff, labels, a branch with screenshots or a fix. The agent acts as `seaport-qa[bot]`, never as a person.

- **One repository, one hour.** Each token is limited to the repository asked for, with the App's permissions as the ceiling (the table above), and expires within the hour. Seaport mints a new one when the old one is close to expiring.
- **Only the agent's docks.** The port answers `POST /crew/github/token` only for the agent's own crew token, and only for a repository where the App of one of the agent's GitHub docks is installed. Any other repository, host or caller gets `404`.
- **Nothing on disk.** The token is asked for each time and passed in the environment.

In a [hosted slot](hosting/hosted-agents.md) it all works already:

- `gh` is Seaport's shim around GitHub's CLI: it finds the repository the command is about (`-R owner/repo`, `GH_REPO`, `gh api repos/owner/repo/…`, or the `origin` remote of the current directory) and runs the real `gh` with the App's token in `GH_TOKEN`. A `GH_TOKEN` of your own wins.
- git's credential helper answers with the App's token for `https://github.com/owner/repo` when the environment names no `credential` for that repository.
- The agent's prompt names the repository and the issue or pull request, and says that `gh` and git act as the App.

```sh
gh pr view 12 -R acme/web --json files,body
gh pr review 12 -R acme/web --request-changes -b "Checkout total is wrong with a coupon; see the table."
gh api repos/acme/web/pulls/12/comments -f body="Total ignores the coupon here" \
  -f commit_id="$(gh pr view 12 -R acme/web --json headRefOid -q .headRefOid)" -f path=src/cart.ts -F line=42 -f side=RIGHT
git push origin HEAD:seaport/qa/12
```

Outside a slot, an agent connected with `seaport connect` gets a token with `seaport github-token owner/repo` (with `SEAPORT_URL` and `SEAPORT_CREW_TOKEN` set): `GH_TOKEN=$(seaport github-token acme/web) gh pr view 12 -R acme/web`.

> [!NOTE]
> A slot reaches only its environment's egress hosts. Add `api.github.com` to `network.egress` for `gh`, and list the repository under `repos` (or add `github.com`) for git. See [Network](hosting/network.md).

## Trigger an agent from a workflow

A GitHub Actions workflow can hand a message to an agent of the dock, for example to run a functional QA review on every pull request that gets the `qa` label. Use the action in this repository:

```yaml
name: QA review
on:
  pull_request:
    types: [labeled]

jobs:
  qa:
    if: github.event.label.name == 'qa'
    runs-on: ubuntu-latest
    permissions:
      id-token: write   # the OIDC token that proves the run to Seaport
    steps:
      - uses: Abraxas-365/seaport/action@main
        with:
          port-url: https://seaport.example.com
          dock: <dock-id>
          agent: qa
          text: |
            Run the functional QA of this pull request on the dev environment
            and leave your review here.
```

No secret goes in the repository. The job asks GitHub for an OIDC token whose audience is your port's URL, and Seaport accepts the message only if:

- GitHub signed the token, it names this port and it hasn't expired,
- the dock's App is installed, and active, on the workflow's repository.

The message lands in the conversation of the pull request (`owner/repo#N`) or of `number` when you set it, as if `@actor` had written it, prefixed with the workflow and run. Customs sees the sender as `@actor` and `github:action`, so `seaport customs -dock <dock-id> deny github:action` turns workflows off.

| Input | Meaning |
| :--- | :--- |
| `port-url` | Your port's `SEAPORT_PUBLIC_URL`. Required. |
| `dock` | The GitHub dock's id. Required. |
| `text` | The message. Required. |
| `agent` | The agent of the dock's team that takes it; empty follows the dock's routing. |
| `number` | The issue or pull request; default the workflow's pull request (`pull_request` events). |
| `id` | Tells several messages of one run apart; a re-run of the same step and attempt isn't sent twice. |

The step's outputs are `ship-id` and `status`: `queued`, or `held` when customs or routing held the message (allow it from the inbox).

## Unlink or delete

**Desconectar la GitHub App** on the dock's page (or `seaport dock unlink <dock-id>`) uninstalls the App from every account it was installed on and forgets its credentials. GitHub has no API to delete an App: delete it in its settings on GitHub if you don't need it anymore. The conversations stay; create a new App from the same dock to use it again. Deleting the dock also deletes its conversations.

## Troubleshooting

- **"GitHub no puede llegar a esta dirección"** on the dock's page: `SEAPORT_PUBLIC_URL` is plain `http` or a loopback address. GitHub would accept the App but never deliver a webhook.
- **Nothing arrives.** In the App's settings on GitHub, **Advanced → Recent deliveries** shows each webhook and Seaport's answer: `401` is a bad signature (the App was created for another dock or port), `404` an App this port forgot (unlinked), `202 ignored` a message that called no agent of the team.
- **The comment waits under customs.** Allow the thread or the dock, as above.
- **Several replicas.** GitHub docks run in every process: any replica can take a webhook or post a comment.
- **`gh` says "no GitHub App token".** The App isn't installed on that repository, the agent isn't on a GitHub dock, or the slot can't reach the port. `seaport github-token owner/repo` shows the port's answer.
- **The workflow step fails with `401`.** The job needs `permissions: id-token: write`, and `port-url` must be exactly the port's `SEAPORT_PUBLIC_URL`. On a fork's pull request GitHub gives no OIDC token.
- **The workflow step fails with `403`.** The dock's App isn't installed on that repository, or is suspended.
- **`gh` can't connect.** Add `api.github.com` to the environment's `network.egress`.

## Where to read more

- [Customs](customs.md)
- [Agents and routing](agents-and-routing.md)
- [HTTP API](../reference/http-api.md#github-docks)
- [Configuration](../reference/configuration.md#github)
