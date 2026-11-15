# Vaults

Give your agents a shared memory that you own: markdown notes the agents read and write through the port, whichever harness or slot they run on.

Without a vault, what an agent remembers lives in its harness: rness keeps notes in each slot's own state volume, and Claude Code keeps none you can share. Two slots of one agent don't share them, two agents don't, and nobody can read or fix them from the web app. A **vault** is a named set of **entries** (one markdown text each) that belongs to your organization. You decide which agents and docks may use it, and each use is either `read` or `write`.

## Create a vault

In the web app, open **Bóvedas** (Vaults) and create one there. From the CLI:

```sh
seaport vault create company -title "About the company" \
  -description "Facts about customers, products and past decisions. Search it before answering."
seaport vault put company support-hours "Support answers 09:00-18:00 CET, Monday to Friday."
seaport vault entries company
seaport vault get company support-hours
```

A vault's name and an entry's name are 1 to 64 lowercase letters, digits, `-` or `_`, starting with a letter or digit. The vault's description is what agents read to decide when to use it, so say what belongs in it. The full list of commands is in the [CLI reference](../reference/cli.md#vaults), and the routes in the [HTTP API](../reference/http-api.md#vaults).

## Give it to agents and docks

A vault does nothing until it is granted. There are two kinds of grant, each with a mode:

| Grant | The agent sees the vault | Use it for |
| :--- | :--- | :--- |
| **To an agent** | Everywhere: on any dock, in any conversation, and in a call that names no conversation. | Memory that belongs to a role: what the `pm` agent knows, wherever it is called. |
| **To a dock** | Only while it works a conversation of that dock. | Memory that belongs to a place: what whoever works the support Slack should know. |

```sh
seaport vault grant company -agent pm -mode write        # pm sees it everywhere
seaport vault grant faq -dock <dock-id> -mode read       # seen while working that dock
seaport vault revoke faq -dock <dock-id>
seaport vault show company                                # who has it, and in which mode
```

The rule, in full: **an agent's own vaults are seen everywhere; a dock's vaults are added while the agent works a conversation of that dock; the widest mode wins.** A dock's vault is not the union of everything the agent's docks hold. Granting a vault to the Slack dock means whoever works Slack sees it; it does not appear in WhatsApp because the same agent is there too. If a vault reaches the agent both ways, say `read` through the dock and `write` through the agent, the agent can write it (`write` includes `read`).

"Working a conversation of that dock" is decided by the call: the agent passes the `conversation_id` the port put in its prompt, and the dock's vaults count only if that conversation belongs to a dock the agent is assigned to. A conversation of any other dock adds nothing.

Revoking a grant, unassigning an agent or deleting a dock removes the access at once; the entries stay in the vault.

## Modes

| Mode | The agent can |
| :--- | :--- |
| `read` | List the vault, search it and read entries. |
| `write` | Everything `read` allows, and save and delete entries. |

Agents never create, rename, grant or delete vaults: that is yours (`vaults:write`).

## What the agent sees

An agent that has any grant, its own or on any dock it works, gets five [MCP tools](../reference/mcp-tools.md#vaults): `vaults`, `memory_search`, `memory_read`, `memory_save` and `memory_delete`. An agent with none sees no vault tools, the same way an agent with no skills sees no `skill` tool.

- `vaults` lists the vaults usable here with their description, the agent's mode and an index of the newest 200 entries (name and first line). The rest is found with a search.
- `memory_search` looks for words in all usable vaults, or in one, and answers hits with a snippet.
- `memory_read` returns an entry in full with its version and who wrote it.
- `memory_save` creates an entry, or edits one when it passes the `base` version it read. If someone saved meanwhile the call fails with a conflict, and the agent reads the entry again and redoes the edit.
- `memory_delete` deletes an entry.

Every tool takes an optional `conversation_id`. Without it only the agent's own vaults are usable; with the conversation it is working, its dock's vaults are added. The port's instructions tell the agent to search memory before answering about the company, a customer or a past decision, and to save durable facts one per entry, with no secrets.

Search is full text over entry names and bodies. It matches words, not meaning.

## Who wrote what

Every save is a **version**, and every version records who wrote it:

- a person: the signed-in user in iam mode, or the token's owner;
- an agent: its name, plus the conversation and dock it was working.

The agent reads this too. `memory_read` and `memory_search` say, for each entry, "written by agent pm from a Slack conversation" or "written by a person", so it can weigh what an agent noted from a chat differently from what a person wrote.

```sh
seaport vault versions company support-hours      # every version, who, when
seaport vault revert company support-hours 2      # saves version 2's text as a new version
```

A revert never rewrites history: the old text becomes the newest version, written by you. Versions of a deleted entry are kept until the vault is deleted, but the admin API reads versions only of entries that exist. To bring a deleted entry back, save it again with its text from `vault pull` or git.

## Secrets are refused

A write whose text looks like a credential (an API key of a known provider, a GitHub or Slack token, a private key, a Seaport crew token) is refused with `400`, naming the kind ("looks like a GitHub token") and never the match. The check applies to people and agents alike. It is a safety net for the common shapes, not a guarantee. Credentials an agent needs go in its [secrets](profiles.md), not in a vault.

## Write access on a dock open to strangers

`write` on a dock is your decision, and Seaport doesn't refuse it. Consider what it means on a dock whose [customs](customs.md) let unknown people in: whoever may wake the agent there can talk it into saving something, and what it saves is read later by everyone the vault reaches. There is no `propose` mode where a person approves each write.

What you get instead is a trail you can act on:

- every entry says it was written by an agent, which one, and from which conversation and dock, so you can trace a suspicious entry to the message that caused it;
- every earlier text is a version, so `seaport vault revert` undoes it;
- the secret check stops the commonest leak;
- the web app notes on a dock's `write` grant when that dock admits strangers.

If you want a vault agents read but only people edit, grant `read`. A reasonable pattern is `write` on a vault for a closed dock (your team's Slack) and `read` on the vault that public docks see.

## Edit with files and git

`pull` and `push` turn a vault into a directory, one `<name>.md` per entry, so you can edit with your editor and keep the history in git:

```sh
seaport vault pull company ./company-vault          # writes company-vault/<name>.md, overwriting
cd company-vault && git init && git add . && git commit -m "snapshot"
$EDITOR support-hours.md pricing.md
seaport vault push company . -prune                 # created 1, updated 1, deleted 0, unchanged 12
```

`push` reads every `*.md` file whose name, without the extension, is a valid entry name; it lists the others and leaves them out. It creates new entries and updates the ones whose text differs, passing the version it just read as `base`, so an agent's save between your `pull` and `push` is a conflict for that entry, reported by name, rather than an overwrite. It counts text without surrounding whitespace as equal. With `-prune` it also deletes the entries that have no file. The exit status is non-zero when any entry failed.

Pushed entries are written as you, like any other person's save.

## Limits

| Limit | Value |
| :--- | :--- |
| Vaults per organization | 64 (`SEAPORT_MAX_VAULTS`, `0` turns vaults off) |
| Entries per vault | 2000 |
| Entry text | 64 KiB |
| Names | 64 characters |
| Entries in the `vaults` tool's index | 200 per vault, newest first |
| Hits of one search | 20 |

Going over a count is refused with `422 QUOTA_EXCEEDED`. All limits are in [Limits](../reference/limits.md#vaults).

## What a vault is not

- It is text only: no files or images in entries.
- It is not semantic search: there are no embeddings.
- It doesn't replace a harness's own memory. rness's `~/.rness/memory` stays per slot and is separate; a vault is the memory that doesn't depend on the harness.

See [Known limitations](../project/known-limitations.md), [Agent profiles](profiles.md), [Permissions](../reference/permissions.md) and [Events](../reference/events.md#event-types).
