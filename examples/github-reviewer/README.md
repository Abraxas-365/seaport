# GitHub reviewer agent

An agent that reviews pull requests when someone mentions it: it reads the diff, runs the checks it can, and leaves a formal review with comments on the lines that matter, as your GitHub App.

What you get:

- `@qa review this` in a pull request (description, comment or review) wakes the agent.
- It answers in the thread within a minute: a short summary, then a GitHub review (`approve`, `comment` or `request changes`) with line comments.
- Optionally, a workflow asks for a review whenever a pull request gets the `review` label, with no secret in the repository.

## Files

| File | What it is |
| :--- | :--- |
| `AGENTS.md` | the agent's instructions (its profile) |
| `skills/review-pr/SKILL.md` | how to review a pull request, step by step |
| `skills/test-plan/SKILL.md` | how to turn a change into a short manual test plan |
| `workflow.yml` | optional: copy to `.github/workflows/` to ask for a review by label |

## Set it up

You need a running port with a public URL and a GitHub dock whose App is installed on the repository ([GitHub guide](../../docs/guides/github.md)).

1. Create the agent and put it on the GitHub dock:

   ```sh
   seaport agents add qa
   seaport agents assign qa <github-dock-id>
   ```

2. Give it this profile. The agent acts through `gh`, so let the connector approve its commands:

   ```sh
   seaport agents profile set qa \
     -instructions-file examples/github-reviewer/AGENTS.md \
     -skills-dir examples/github-reviewer/skills \
     -approve allow
   ```

3. Let the repository in. On a public repository, deny outsiders so only members and collaborators can wake the agent:

   ```sh
   seaport customs -dock <github-dock-id> default allow
   seaport customs -dock <github-dock-id> deny github:outsider
   ```

4. Connect a harness with a checkout of the repository as its working directory:

   ```sh
   seaport crew tokens create qa -agent qa      # shows the token once
   cd ~/code/my-repo
   SEAPORT_URL=https://seaport.example.com SEAPORT_CREW_TOKEN=sp_crew_… \
     seaport connect -name qa -- rness --acp -m anthropic/claude-sonnet-4-5
   ```

   Or host it on the port ([hosted agents](../../docs/guides/hosting/hosted-agents.md)) with the repository under `repos` and `api.github.com` in `network.egress`.

5. Open a pull request and write `@qa review this`.

## Review by label (optional)

Copy `workflow.yml` to `.github/workflows/seaport-review.yml` and set `port-url` and `dock`. Adding the `review` label to a pull request then hands it to `qa`. The job proves itself with GitHub's OIDC token; Seaport checks it names your port and that your App is installed on the repository ([details](../../docs/guides/github.md#trigger-an-agent-from-a-workflow)).

## Make it yours

- Edit `AGENTS.md` with your team's rules: what blocks a merge, which commands run the tests, the tone of comments.
- Add a skill per recurring job (`skills/<name>/SKILL.md`, a one-line `description` saying when to use it).
- Re-run `seaport agents profile set qa …`: connected agents pick up the new profile after their current turn.
