#!/usr/bin/env bash
# Upgrade smoke test: data written by one Seaport image must survive the
# next image starting on the same database (migrations apply, rows read back).
#
#   scripts/upgrade-smoke.sh OLD_IMAGE NEW_IMAGE
#
# It starts its own Postgres and a private Docker network, runs OLD_IMAGE
# (SEAPORT_AUTH=token), seeds data through the HTTP API (an api dock, an
# agent on its team, a customs rule, one inbound message), stops it, starts
# NEW_IMAGE on the same database and checks /healthz and that everything
# reads back. Needs docker, curl and jq. Everything it creates is removed on
# exit. Example data only.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 OLD_IMAGE NEW_IMAGE" >&2
  exit 2
fi
old_image="$1"
new_image="$2"

for tool in docker curl jq openssl; do
  command -v "$tool" >/dev/null || { echo "missing: $tool" >&2; exit 2; }
done

run="upgrade-smoke-$$"
net="$run"
pg="$run-pg"
app="$run-app"
token="smoke-$(openssl rand -hex 16)"
secret_key="$(openssl rand -base64 32)"
# Same key and data volume for both versions: sealed data must open again.
data="$run-data"
db_url="postgres://seaport:seaport@$pg:5432/seaport?sslmode=disable"

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "--- logs of $app" >&2
    docker logs --tail 80 "$app" >&2 2>&1 || true
  fi
  docker rm -f "$app" "$pg" >/dev/null 2>&1 || true
  docker volume rm "$data" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

docker network create "$net" >/dev/null
docker volume create "$data" >/dev/null
# The image's /data is owned by uid 65532 (distroless nonroot).
docker run --rm -v "$data:/data" --user 0 --entrypoint /bin/sh busybox:stable -c 'chown 65532:65532 /data' >/dev/null 2>&1 || true

docker run -d --name "$pg" --network "$net" \
  -e POSTGRES_USER=seaport -e POSTGRES_PASSWORD=seaport -e POSTGRES_DB=seaport \
  postgres:16-alpine >/dev/null
# The image's init-time server listens on its socket only (then restarts):
# ready means answering over TCP.
ready=""
for _ in $(seq 1 60); do
  if docker exec "$pg" pg_isready -h 127.0.0.1 -U seaport -d seaport >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
[ -n "$ready" ] || fail "postgres did not become ready"

base=""
# start IMAGE: runs Seaport on the shared database and sets $base.
start() {
  docker rm -f "$app" >/dev/null 2>&1 || true
  docker run -d --name "$app" --network "$net" -p 127.0.0.1::8740 -v "$data:/data" \
    -e SEAPORT_AUTH=token -e SEAPORT_ADMIN_TOKEN="$token" \
    -e DATABASE_URL="$db_url" -e SEAPORT_SECRET_KEY="$secret_key" \
    "$1" >/dev/null
  local port
  port="$(docker port "$app" 8740/tcp | head -n1 | sed 's/.*://')"
  base="http://127.0.0.1:$port"
  for _ in $(seq 1 90); do
    if [ "$(docker inspect -f '{{.State.Running}}' "$app")" != true ]; then
      fail "$1 exited while starting"
    fi
    if curl -fsS "$base/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "$1: /healthz did not answer in 90s"
}

# api METHOD PATH [JSON]: calls the admin API, fails on a non-2xx answer and
# prints the body.
api() {
  local method="$1" path="$2" body="${3:-}" out code
  out="$(mktemp)"
  if [ -n "$body" ]; then
    code="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "$base$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "$body")"
  else
    code="$(curl -sS -o "$out" -w '%{http_code}' -X "$method" "$base$path" \
      -H "Authorization: Bearer $token")"
  fi
  case "$code" in
    2??) cat "$out"; rm -f "$out" ;;
    *) echo "$method $path -> $code: $(cat "$out")" >&2; rm -f "$out"; return 1 ;;
  esac
}

echo "== old: $old_image"
start "$old_image"
old_health="$(curl -fsS "$base/healthz")"
echo "$old_health"

dock="$(api POST /api/v1/docks '{"channel":"api","name":"smoke-support"}' | jq -er .id)"
agent="$(api POST /api/v1/agents '{"name":"smoke-agent","description":"upgrade smoke"}' | jq -er .id)"
api PUT "/api/v1/docks/$dock/agents/$agent" '{"default":true}' >/dev/null
rule="$(api POST "/api/v1/docks/$dock/customs/rules" '{"effect":"allow","pattern":"+34600000001"}' | jq -er .id)"
api POST "/api/v1/docks/$dock/ships" '{
  "chat_id": "smoke-chat-1",
  "chat_title": "Smoke chat",
  "message_id": "smoke-msg-1",
  "sender_id": "+34600000001",
  "sender_name": "Ana",
  "text": "hello from before the upgrade"
}' >/dev/null
echo "seeded dock=$dock agent=$agent rule=$rule"

# The seed reads back on the old version too: a failure after the upgrade
# then points at the upgrade, not at the seed.
conv="$(api GET "/api/v1/conversations?dock_id=$dock" | jq -er '.items[] | select(.title == "Smoke chat") | .id')"

docker stop "$app" >/dev/null
echo "== new: $new_image"
start "$new_image"
new_health="$(curl -fsS "$base/healthz")"
echo "$new_health"
[ "$(echo "$new_health" | jq -r .status)" = ok ] || fail "/healthz is not ok: $new_health"

api GET /api/v1/docks | jq -e --arg id "$dock" '.items[] | select(.id == $id and .name == "smoke-support" and .channel == "api")' >/dev/null ||
  fail "the dock is gone after the upgrade"
api GET /api/v1/agents | jq -e --arg id "$agent" '.items[] | select(.id == $id and .name == "smoke-agent")' >/dev/null ||
  fail "the agent is gone after the upgrade"
api GET "/api/v1/docks/$dock/agents" | jq -e --arg id "$agent" '.agents[] | select(.agent_id == $id and .default == true)' >/dev/null ||
  fail "the dock's team lost its agent"
api GET "/api/v1/docks/$dock/customs" | jq -e --arg id "$rule" '.rules[] | select(.id == $id and .effect == "allow" and .pattern == "+34600000001")' >/dev/null ||
  fail "the customs rule is gone after the upgrade"
api GET "/api/v1/conversations?dock_id=$dock" | jq -e --arg id "$conv" '.items[] | select(.id == $id and .title == "Smoke chat")' >/dev/null ||
  fail "the conversation is gone after the upgrade"
api GET "/api/v1/conversations/$conv/ships" | jq -e '.items[] | select(.text == "hello from before the upgrade")' >/dev/null ||
  fail "the message is gone after the upgrade"

# The new version still takes writes on the old data: the same message id is
# a duplicate, a new one is stored.
dup="$(api POST "/api/v1/docks/$dock/ships" '{"chat_id":"smoke-chat-1","message_id":"smoke-msg-1","sender_id":"+34600000001","text":"again"}' | jq -r .duplicate)"
[ "$dup" = true ] || fail "a repeated message id was not a duplicate (duplicate=$dup)"
api POST "/api/v1/docks/$dock/ships" '{"chat_id":"smoke-chat-1","message_id":"smoke-msg-2","sender_id":"+34600000001","text":"after the upgrade"}' >/dev/null

echo "OK: $old_image -> $new_image"
