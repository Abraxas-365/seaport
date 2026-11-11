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
| Contents: read and write | for agents that push branches (docs, screenshots) |
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

Seaport forwards a message only when it mentions an agent of the dock's team (`@qa`) or the App itself (`@seaport-qa`); everything else is ignored. Mentioning the App routes like a message that names no agent: to the agent already working that thread, the only agent of the team, or the default one. Comments by bots, including the App's own replies, are ignored, so agents never wake each other in a loop.

The agent gets the thread as a group conversation titled `PR acme/web#12: <title>` (or `Issue …`); review text starts with the verdict (`(review: changes_requested) …`) and line comments with their place (`(on src/pay.ts:12) …`). Seaport reacts with 👀 on the comment once an agent has it. GitHub's redeliveries are recognized and stored once.

## The answer

The agent answers with the MCP `reply` tool, as on any channel: Seaport posts the text as a comment on the thread, with the App's identity (`seaport-qa[bot]`), within the dock's [send limits](../reference/limits.md). You see the whole thread in the web app's inbox too.

Formal reviews, line comments, labels and pushes are not `reply`'s job: hosted agents will use `gh` and git with a short-lived token of the App for that.

## Unlink or delete

**Desconectar la GitHub App** on the dock's page (or `seaport dock unlink <dock-id>`) uninstalls the App from every account it was installed on and forgets its credentials. GitHub has no API to delete an App: delete it in its settings on GitHub if you don't need it anymore. The conversations stay; create a new App from the same dock to use it again. Deleting the dock also deletes its conversations.

## Troubleshooting

- **"GitHub no puede llegar a esta dirección"** on the dock's page: `SEAPORT_PUBLIC_URL` is plain `http` or a loopback address. GitHub would accept the App but never deliver a webhook.
- **Nothing arrives.** In the App's settings on GitHub, **Advanced → Recent deliveries** shows each webhook and Seaport's answer: `401` is a bad signature (the App was created for another dock or port), `404` an App this port forgot (unlinked), `202 ignored` a message that called no agent of the team.
- **The comment waits under customs.** Allow the thread or the dock, as above.
- **Several replicas.** GitHub docks run in every process: any replica can take a webhook or post a comment.

## Where to read more

- [Customs](customs.md)
- [Agents and routing](agents-and-routing.md)
- [HTTP API](../reference/http-api.md#github-docks)
- [Configuration](../reference/configuration.md#github)
