#!/usr/bin/env bash
# iamkit-provision.sh — idempotently provision Seaport (SEAPORT_AUTH=iam) in
# ANY IAMKit (local, staging, production) and write its IAMKIT_* settings to
# an env file (docs/guides/operations/iam-mode.md): `set -a; . ./.iamkit.env; set +a`.
#
# Everything is found by name and created only when missing, so re-running it
# after editing iam/permissions.json (new permissions, new roles) syncs IAMKit.
# From the iamkit skill's generic script, plus organization API keys (the
# CLI's credential in iam mode). The iam e2e tests (internal/bootstrap,
# SEAPORT_TEST_IAMKIT=1) provision the same shape.
#
# Creates / syncs:
#   1. project + environment
#   2. organization (the first tenant)
#   3. application + resource (prefix, audience, permission catalog) + binding
#   4. roles on the resource (spec .roles)
#   5. public OAuth client for the browser app (code + PKCE; hosted login
#      unless HOSTED_LOGIN=0), with redirect/logout URIs and allowed origins
#   6. organization API keys enabled, allowed every Seaport permission
#   7. optional first user (OWNER_EMAIL [+ OWNER_PASSWORD]): member of the
#      organization with the first role of the spec (Admin)
#
# Spec / permission manifest (SPEC=path, JSON) — the single source of the app's
# permissions (iam/permissions.json; Go and TypeScript constants are generated
# from it):
#   {
#     "resource": {"name": "Seaport API", "prefix": "seaport"},
#     "permissions": [{"name": "seaport:docks:read", "label": "View docks"}, ...],
#     "roles": [{"name": "Admin", "permissions": ["seaport:docks:read", ...]}, ...]
#   }
#   Permissions are strings or {name, label} objects and must start with
#   "<prefix>:"; role permissions must be in the catalog.
#
# Usage: scripts/iamkit-provision.sh [--dry-run] [--prune]
#   --dry-run  print what would change; write nothing (not even OUT)
#   --prune    also remove permissions that left the spec, from the catalog and
#              from the spec's roles. Without it they are KEPT: IAMKit strips a
#              permission removed from the catalog from every role, grant and
#              API key at once, so prune only after no deployed Seaport checks
#              it (rename = add new, sync, deploy, sync --prune).
#
# Variables (defaults in brackets):
#   IAMKIT_URL        IAMKit base URL, as this script and Seaport reach it [http://localhost:8080]
#   IAMKIT_PUBLIC_URL IAMKit as browsers reach it, when different      [$IAMKIT_URL]
#   IAMKIT_KEY        management key (ik_mgmt_…); or IAMKIT_KEY_FILE with .management_key
#   SPEC              manifest file                  [iam/permissions.json]
#   APP_URL           Seaport's SEAPORT_PUBLIC_URL                  [http://127.0.0.1:8740]
#   AUDIENCE          resource audience (immutable once created!)   [$APP_URL]
#   CALLBACK_PATH     OAuth redirect path on APP_URL                 [/auth/callback]
#   EXTRA_ORIGINS     comma-separated extra app origins (e.g. pnpm dev http://localhost:5173)
#   PROJECT_NAME [Seaport]  ENVIRONMENT_NAME [Development]  ORG_NAME [Demo Org]  APP_NAME [Seaport Web]
#   HOSTED_LOGIN      1 = IAMKit's hosted sign-in pages, 0 = custom UI  [1]
#   ORG_KEY_LIMIT     organization API keys per organization          [20]
#   OWNER_EMAIL / OWNER_PASSWORD (12-72 chars) / OWNER_NAME
#   OUT               env file receiving the settings               [.iamkit.env]
#
# OUT holds no secrets (Seaport's SEAPORT_SECRET_KEY is yours to generate),
# but keep it out of git all the same (ids of your tenancy). Never run with
# `set -x`: the management key is in play.
set -euo pipefail

command -v curl >/dev/null && command -v jq >/dev/null || { echo "curl and jq are required" >&2; exit 1; }

DRY_RUN=0 PRUNE=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --prune) PRUNE=1 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $arg (try --help)" >&2; exit 2 ;;
  esac
done

IAMKIT_URL=${IAMKIT_URL:-http://localhost:8080}; IAMKIT_URL=${IAMKIT_URL%/}
IAMKIT_PUBLIC_URL=${IAMKIT_PUBLIC_URL:-$IAMKIT_URL}; IAMKIT_PUBLIC_URL=${IAMKIT_PUBLIC_URL%/}
APP_URL=${APP_URL:-http://127.0.0.1:8740}; APP_URL=${APP_URL%/}
AUDIENCE=${AUDIENCE:-$APP_URL}
CALLBACK_PATH=${CALLBACK_PATH:-/auth/callback}
SPEC=${SPEC:-iam/permissions.json}
PROJECT_NAME=${PROJECT_NAME:-Seaport}
ENVIRONMENT_NAME=${ENVIRONMENT_NAME:-Development}
ORG_NAME=${ORG_NAME:-Demo Org}
APP_NAME=${APP_NAME:-Seaport Web}
HOSTED_LOGIN=${HOSTED_LOGIN:-1}
ORG_KEY_LIMIT=${ORG_KEY_LIMIT:-20}
OUT=${OUT:-.iamkit.env}

KEY=${IAMKIT_KEY:-}
if [[ -z "$KEY" && -n "${IAMKIT_KEY_FILE:-}" ]]; then KEY=$(jq -er .management_key "$IAMKIT_KEY_FILE"); fi
[[ -n "$KEY" ]] || { echo "Set IAMKIT_KEY (or IAMKIT_KEY_FILE)" >&2; exit 1; }
# Keys are ik_mgmt_ + url-safe characters; anything else (spaces, quotes,
# backslashes, newlines) is a paste error and would break the curl config.
[[ "$KEY" =~ ^[A-Za-z0-9._~+/=-]+$ ]] || { echo "IAMKIT_KEY has unexpected characters (paste error?)" >&2; exit 1; }
[[ -f "$SPEC" ]] || { echo "Spec $SPEC not found (set SPEC=path)" >&2; exit 1; }
jq -e . "$SPEC" >/dev/null || { echo "Spec $SPEC is not valid JSON" >&2; exit 1; }
[[ "$ORG_KEY_LIMIT" =~ ^[1-9][0-9]*$ ]] || { echo "ORG_KEY_LIMIT must be a positive integer" >&2; exit 1; }
# The spec must be coherent before anything is sent.
problems=$(jq -r '
  (.resource.prefix // "") as $p
  | [.permissions[]? | if type == "string" then . else .name end] as $c
  | (if $p == "" then ["resource.prefix is required"] else [] end)
  + [$c[] | select(startswith($p + ":") | not) | "permission \(.) does not start with \($p):"]
  + [$c | group_by(.)[] | select(length > 1) | "permission \(.[0]) is listed twice"]
  + [.roles[]? as $r | $r.permissions[] | select(IN($c[]) | not) | "role \($r.name): \(.) is not in the catalog"]
  | .[]' "$SPEC")
[[ -z "$problems" ]] || { echo "Spec $SPEC is invalid:" >&2; sed 's/^/  /' <<<"$problems" >&2; exit 1; }

log() { echo "  $*" >&2; }
# change: a log line for a write ("[dry-run]" marked when nothing is written).
change() { if (( DRY_RUN )); then echo "  [dry-run] $*" >&2; else echo "  $*" >&2; fi; }
step() { echo "$*" >&2; }
q() { jq -Rn --arg v "$1" '$v'; } # JSON string literal for jq filters
(( DRY_RUN )) && echo "Dry run: nothing is written." >&2

# The management key reaches curl through a private config file (mktemp:
# mode 0600), never its command line (visible to every user in ps).
CURL_AUTH=$(mktemp "${TMPDIR:-/tmp}/iamkit-provision.XXXXXX")
trap 'rm -f "$CURL_AUTH"' EXIT
printf 'header = "X-API-Key: %s"\n' "$KEY" >"$CURL_AUTH"

# api METHOD PATH < body -> response body; fails on non-2xx with IAMKit's error.
api() {
  curl --fail-with-body --silent --show-error --request "$1" "$IAMKIT_URL/management/v1$2" \
    --config "$CURL_AUTH" -H 'Content-Type: application/json' --data-binary @-
}
# write is api for changes: a dry run sends nothing and answers placeholder ids.
write() {
  if (( DRY_RUN )); then cat >/dev/null; echo '{"id":"dry-run","client_id":"dry-run","secret":""}'
  else api "$@"; fi
}
# get: reads; paths under objects a dry run did not create answer an empty list.
get() { if [[ "$1" == *dry-run* ]]; then echo '[]'; else api GET "$1" </dev/null; fi; }
# changes OLD NEW (JSON arrays) -> "+a -b" or empty.
changes() {
  jq -rn --argjson o "$1" --argjson n "$2" \
    '[($n - $o | map("+" + .)), ($o - $n | map("-" + .))] | add | join(" ")'
}
# all PATH -> every item of a list (paged {items,page} or plain array) as one array.
all() {
  local sep='?' offset=0 page total items='[]'
  [[ "$1" == *\?* ]] && sep='&'
  while :; do
    page=$(get "$1${sep}limit=100&offset=$offset")
    items=$(jq -c --argjson p "$page" '. + (if ($p | type) == "object" then $p.items else $p end)' <<<"$items")
    total=$(jq -r 'if type == "object" then .page.total // empty else empty end' <<<"$page")
    offset=$((offset + 100))
    [[ -n "$total" && "$offset" -lt "$total" ]] || break
  done
  echo "$items"
}
# pick JSON-ARRAY JQ-FILTER -> id of the first match, or empty.
pick() { jq -r "[.[] | select($2)][0].id // empty" <<<"$1"; }

get /me >/dev/null || { echo "Management key rejected by $IAMKIT_URL" >&2; exit 1; }

step "1. Project and environment"
project=$(pick "$(get /projects)" ".name == $(q "$PROJECT_NAME")")
if [[ -z "$project" ]]; then
  project=$(jq -n --arg n "$PROJECT_NAME" '{name:$n}' | write POST /projects | jq -er .id); change "created project $PROJECT_NAME"
fi
environment=$(pick "$(get "/projects/$project/environments")" ".name == $(q "$ENVIRONMENT_NAME")")
if [[ -z "$environment" ]]; then
  environment=$(jq -n --arg n "$ENVIRONMENT_NAME" '{name:$n}' | write POST "/projects/$project/environments" | jq -er .id); change "created environment $ENVIRONMENT_NAME"
fi
e="/environments/$environment"
log "environment $environment"

step "2. Organization"
organization=$(pick "$(all "$e/organizations")" ".name == $(q "$ORG_NAME")")
if [[ -z "$organization" ]]; then
  organization=$(jq -n --arg n "$ORG_NAME" '{name:$n}' | write POST "$e/organizations" | jq -er .id); change "created organization $ORG_NAME"
fi
log "organization $organization"

step "3. Application, resource (permission catalog), binding"
callback="$APP_URL$CALLBACK_PATH"
application=$(pick "$(all "$e/applications")" ".name == $(q "$APP_NAME")")
if [[ -z "$application" ]]; then
  application=$(jq -n --arg n "$APP_NAME" --arg r "$callback" '{name:$n,redirect_uris:[$r]}' | write POST "$e/applications" | jq -er .id); change "created application $APP_NAME"
fi
res_name=$(jq -er .resource.name "$SPEC"); prefix=$(jq -er .resource.prefix "$SPEC")
wanted=$(jq -c '[.permissions[] | if type == "string" then . else .name end]' "$SPEC")
stale='[]'
resources=$(all "$e/resources")
resource=$(pick "$resources" ".prefix == $(q "$prefix")")
if [[ -z "$resource" ]]; then
  resource=$(jq -n --arg n "$res_name" --arg p "$prefix" --arg a "$AUDIENCE" --argjson perms "$wanted" \
    '{name:$n,prefix:$p,audience:$a,permissions:$perms}' | write POST "$e/resources" | jq -er .id)
  change "created resource $res_name (audience $AUDIENCE, $(jq length <<<"$wanted") permissions)"
else
  current=$(jq -r --arg id "$resource" '.[] | select(.id == $id) | .audience' <<<"$resources")
  [[ "$current" == "$AUDIENCE" ]] || { log "NOTE: resource audience is $current (immutable); using it"; AUDIENCE=$current; }
  have=$(jq -c --arg id "$resource" '.[] | select(.id == $id) | .permissions // []' <<<"$resources")
  stale=$(jq -cn --argjson h "$have" --argjson w "$wanted" '$h - $w')
  # Without --prune, permissions that left the spec stay in the catalog: IAMKit
  # would strip them from every role, grant and service account (no error).
  catalog=$(if (( PRUNE )); then echo "$wanted"; else jq -cn --argjson w "$wanted" --argjson s "$stale" '$w + $s'; fi)
  diff=$(changes "$have" "$catalog")
  if [[ -n "$diff" ]]; then
    jq -n --arg n "$res_name" --argjson perms "$catalog" '{name:$n,permissions:$perms}' | write PUT "$e/resources/$resource" >/dev/null
    change "catalog: $diff"
  else log "catalog: $(jq length <<<"$catalog") permissions, unchanged"; fi
fi
linked=$(get "$e/applications/$application/resources" | jq -r --arg r "$resource" '[(if type == "object" then .items else . end)[] | select((.id // .resource_id) == $r)] | length')
if [[ "$linked" == 0 ]]; then
  jq -n --arg a "$application" --arg r "$resource" '{application_id:$a,resource_id:$r}' | write POST "$e/application-resources" >/dev/null
  change "linked $APP_NAME -> $res_name"
fi

step "4. Roles"
role_lines=()
first_role=""
roles=$(all "$e/roles")
while IFS= read -r spec_role; do
  [[ -n "$spec_role" ]] || continue
  name=$(jq -r .name <<<"$spec_role")
  perms=$(jq -c .permissions <<<"$spec_role")
  id=$(pick "$roles" ".resource_id == $(q "$resource") and .name == $(q "$name")")
  if [[ -z "$id" ]]; then
    id=$(jq -n --arg n "$name" --arg r "$resource" --argjson p "$perms" '{name:$n,resource_id:$r,permissions:$p}' |
      write POST "$e/roles" | jq -er .id)
    change "created role $name"
  else
    have=$(jq -c --arg id "$id" '.[] | select(.id == $id) | .permissions // []' <<<"$roles")
    # A role keeps the stale permissions it holds until --prune.
    (( PRUNE )) || perms=$(jq -cn --argjson p "$perms" --argjson h "$have" --argjson s "$stale" '$p + ($h - ($h - $s)) | unique')
    diff=$(changes "$have" "$perms")
    if [[ -n "$diff" ]]; then
      jq -n --arg n "$name" --arg r "$resource" --argjson p "$perms" '{name:$n,resource_id:$r,permissions:$p}' |
        write PUT "$e/roles/$id" >/dev/null
      change "role $name: $diff"
    else log "role $name"; fi
  fi
  [[ -n "$first_role" ]] || first_role=$id
  slug=$(tr '[:lower:]' '[:upper:]' <<<"$name" | tr -c 'A-Z0-9\n' '_')
  role_lines+=("IAMKIT_ROLE_${slug}_ID=$id")
done < <(jq -c '.roles // [] | .[]' "$SPEC")
# Roles that are not in the spec are left alone (they may be tenant-made).

step "5. Browser OAuth client (code + PKCE)"
origins=$(jq -cn --arg p "$APP_URL" --arg extra "${EXTRA_ORIGINS:-}" \
  '[$p] + ($extra | split(",") | map(gsub("^\\s+|\\s+$"; "") | sub("/+$"; "")) | map(select(. != ""))) | unique')
hosted=$([[ "$HOSTED_LOGIN" == 1 ]] && echo true || echo false)
web_body=$(jq -c --arg cb "$CALLBACK_PATH" --argjson h "$hosted" \
  '{redirect_uris: map(. + $cb), post_logout_redirect_uris: map(. + "/"), allowed_origins: ., hosted_login: $h}' <<<"$origins")
clients=$(all "$e/oauth-clients?application_id=$application")
web=$(pick "$clients" ".public == true and .resource_id == $(q "$resource")")
if [[ -z "$web" ]]; then
  web=$(jq -c --arg a "$application" --arg r "$resource" \
    '. + {application_id:$a, resource_id:$r, public:true, grant_types:["authorization_code","refresh_token"]}' <<<"$web_body" |
    write POST "$e/oauth-clients" | jq -er .client_id)
  change "created web client $web"
else
  current=$(jq -c --arg id "$web" '.[] | select(.id == $id) | {redirect_uris, post_logout_redirect_uris, allowed_origins, hosted_login}' <<<"$clients")
  if [[ "$(jq -S . <<<"$current")" != "$(jq -S . <<<"$web_body")" ]]; then
    write PATCH "$e/oauth-clients/$web" <<<"$web_body" >/dev/null; change "synced web client $web"
  else log "web client $web"; fi
fi

step "6. Organization API keys (the CLI's credential)"
# The policy is environment-wide: only organization_keys (and a limit floor)
# change; user keys, TTL and per-user limits stay as they are.
policy=$(get "$e/api-key-policy" | jq -c 'if type == "object" then . else {} end') # {} in a dry run of a new environment
want_policy=$(jq -c --argjson n "$ORG_KEY_LIMIT" \
  '{user_keys, organization_keys: true, max_ttl_days, max_per_user,
    max_per_organization: ([.max_per_organization // 0, $n] | max)}' <<<"$policy")
if [[ "$(jq -c '{user_keys, organization_keys, max_ttl_days, max_per_user, max_per_organization}' <<<"$policy")" != "$want_policy" ]]; then
  write PUT "$e/api-key-policy" <<<"$want_policy" >/dev/null; change "enabled organization API keys (max $(jq .max_per_organization <<<"$want_policy") per organization)"
else log "organization API keys enabled"; fi
key_perms=$(jq -c --arg id "$resource" '.[] | select(.id == $id) | .api_key_permissions.organization // []' <<<"$resources")
key_wanted=$(if (( PRUNE )); then echo "$wanted"; else jq -cn --argjson w "$wanted" --argjson s "$stale" '$w + $s | unique'; fi)
diff=$(changes "${key_perms:-[]}" "$key_wanted")
if [[ -n "$diff" ]]; then
  jq -cn --argjson p "$key_wanted" '{user: [], organization: $p}' | write PUT "$e/resources/$resource/api-key-permissions" >/dev/null
  change "organization API keys may hold: $diff"
else log "organization API key permissions unchanged"; fi

if [[ -n "${OWNER_EMAIL:-}" ]]; then
  step "7. First user $OWNER_EMAIL"
  user=$(get "$e/users?search=$(jq -rn --arg v "$OWNER_EMAIL" '$v|@uri')&limit=100" |
    jq -r --arg m "$OWNER_EMAIL" '[(if type == "object" then .items else . end)[] | select(.email | ascii_downcase == ($m | ascii_downcase))][0].id // empty')
  if [[ -z "$user" && -n "${OWNER_PASSWORD:-}" ]]; then
    user=$(jq -n --arg m "$OWNER_EMAIL" --arg pw "$OWNER_PASSWORD" --arg n "${OWNER_NAME:-${OWNER_EMAIL%@*}}" \
      '{name:$n,email:$m,password:$pw}' | write POST "$e/users" | jq -er .id); change "created user"
  fi
  if [[ -z "$user" ]]; then
    log "user not found and OWNER_PASSWORD unset: skipped (invite them instead)"
  else
    members=$(all "$e/organizations/$organization/members")
    if [[ -z "$(jq -r --arg u "$user" '[.[] | select((.user_id // .id) == $u)][0] // empty' <<<"$members")" ]]; then
      jq -n --arg o "$organization" --arg u "$user" '{organization_id:$o,user_id:$u}' | write POST "$e/memberships" >/dev/null; change "added to $ORG_NAME"
    fi
    if [[ -n "$first_role" ]]; then
      held=$(all "$e/role-assignments?organization_id=$organization&user_id=$user&role_id=$first_role" | jq length)
      if [[ "$held" == 0 ]]; then
        jq -n --arg o "$organization" --arg u "$user" --arg r "$first_role" '{organization_id:$o,user_id:$u,role_id:$r}' |
          write POST "$e/role-assignments" >/dev/null
        change "assigned the first role"
      fi
    fi
  fi
fi

if [[ "$(jq length <<<"$stale")" != 0 ]]; then
  if (( PRUNE )); then step "Pruned (no longer in $SPEC): $(jq -r 'join(", ")' <<<"$stale")"
  else step "Kept (no longer in $SPEC; remove with --prune once no deployed code checks them): $(jq -r 'join(", ")' <<<"$stale")"; fi
fi
if (( DRY_RUN )); then step "Dry run: nothing written."; exit 0; fi

issuer=$(curl --fail --silent --show-error "$IAMKIT_URL/.well-known/openid-configuration" | jq -er .issuer)
out_name=$(basename "$OUT")
umask 077
{
  echo "# written by scripts/iamkit-provision.sh — keep out of git"
  echo "# load: set -a; . ./$out_name; set +a  (and set SEAPORT_SECRET_KEY yourself)"
  echo "SEAPORT_AUTH=iam"
  echo "SEAPORT_PUBLIC_URL=$APP_URL"
  echo "IAMKIT_URL=$IAMKIT_URL"
  [[ "$IAMKIT_PUBLIC_URL" == "$IAMKIT_URL" ]] || echo "IAMKIT_PUBLIC_URL=$IAMKIT_PUBLIC_URL"
  echo "IAMKIT_ISSUER=$issuer"
  echo "IAMKIT_ENVIRONMENT_ID=$environment"
  echo "IAMKIT_ORGANIZATION_ID=$organization"
  echo "IAMKIT_APPLICATION_ID=$application"
  echo "IAMKIT_RESOURCE_ID=$resource"
  echo "IAMKIT_AUDIENCE=$AUDIENCE"
  echo "IAMKIT_WEB_CLIENT_ID=$web"
  for l in "${role_lines[@]+"${role_lines[@]}"}"; do echo "$l"; done
} >"$OUT"
chmod 600 "$OUT"
step "Done -> $OUT"
