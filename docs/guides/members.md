# Members and access

Invite people, change their role and issue API keys for your organization from the web app, in `iam` mode.

The page is **Miembros y accesos**, under **Miembros** in the side menu. It exists only when the port runs with `SEAPORT_AUTH=iam` and the members settings of [IAM mode](operations/iam-mode.md#manage-people-from-seaport), and only people who may administer the organization see it. Nobody else gets the menu item, and the routes answer `403`.

Seaport acts **as you** against IAMKit: what you can do here is what IAMKit lets you do in your organization. Seaport changes only its own roles and leaves the rest alone.

## Miembros

One row per person: name, email, their Seaport role (`Admin`, `Operator`, `Viewer`, or none) and whether they **can manage members**. Roles IAMKit holds that aren't Seaport's show as read-only badges.

- **Change the role** or the **Puede gestionar miembros** switch of a person. It applies at their next token refresh, 15 minutes at most, or when they sign in again.
- **Remove** a person from the organization. It applies on their next request.

You can't change your own access or remove yourself: ask another administrator. You can't give a role that holds permissions you lack.

## Invitaciones

**Invitar** takes an email, a role and whether the person manages members. IAMKit may email the invitation; when it doesn't, the dialog shows the accept **link** so you can send it yourself. Pending invitations can be **resent** (a new link) or cancelled. When the person accepts, they appear under Miembros with the role you chose.

## Llaves de API

Organization API keys for the CLI (`SEAPORT_ADMIN_TOKEN=ik_key_…`, see [IAM mode](operations/iam-mode.md#sign-in)). **Nueva llave** takes a name, one of the Seaport roles (the key holds that role's permissions as they are today) and a lifetime, from one hour to a year, or none. The value is shown **once**, when you create it: copy it then. The list shows each key's name, role, last characters, state and last use, and **Revocar** stops it at once.

A role change never touches an existing key: revoke it and create another.

## Rules worth knowing

- Only people sign in here. An API key or an agent token can't use these routes (`403`, "sign in as a person").
- Everything is per organization: you never see or change another organization's people or keys.
- See [HTTP API](../reference/http-api.md#members-and-access-iam-mode-optional) for the routes.
