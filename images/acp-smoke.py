#!/usr/bin/env python3
"""ACP smoke of a Seaport base image (CI, job images).

Starts the image's harness the way `seaport agent-run` does (hosted:
SEAPORT_SLOT, the gateway and the slot token as the Anthropic key; no
network, read-only root), then checks it answers initialize and opens a
session: what a slot needs before its first message.

    images/acp-smoke.py IMAGE rness|claude-code
"""

import json
import queue
import subprocess
import sys
import threading

TIMEOUT = 60

COMMANDS = {
    "rness": ["rness", "--acp"],
    "claude-code": ["claude-agent-acp"],
}


def main():
    image, harness = sys.argv[1], sys.argv[2]
    command = COMMANDS[harness]
    proc = subprocess.Popen(
        # the home as an anonymous volume, seeded from the image like the
        # slot's home volume
        ["docker", "run", "--rm", "-i", "--network", "none", "--read-only",
         "--tmpfs", "/tmp", "-v", "/home/agent",
         "-e", "SEAPORT_SLOT=ci-1",
         "-e", "SEAPORT_GATEWAY=http://127.0.0.1:9/gateway",
         "-e", "ANTHROPIC_BASE_URL=http://127.0.0.1:9/gateway/anthropic",
         "-e", "ANTHROPIC_API_KEY=sp_crew_ci",
         "--entrypoint", command[0], image, *command[1:]],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    lines = queue.Queue()

    def read():
        for line in proc.stdout:
            lines.put(line)
        lines.put(None)

    threading.Thread(target=read, daemon=True).start()

    def call(id, method, params):
        proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": id, "method": method, "params": params}) + "\n")
        proc.stdin.flush()
        while True:
            try:
                line = lines.get(timeout=TIMEOUT)
            except queue.Empty:
                sys.exit(f"{method}: no answer in {TIMEOUT}s")
            if line is None:
                sys.exit(f"{method}: the harness exited")
            msg = json.loads(line)
            if msg.get("id") != id or "method" in msg:
                continue  # notifications, requests of the agent
            if "error" in msg:
                sys.exit(f"{method}: {msg['error']}")
            return msg["result"]

    try:
        init = call(1, "initialize", {"protocolVersion": 1, "clientCapabilities": {}})
        print("initialize:", json.dumps(init.get("agentInfo", {})))
        params = {"cwd": "/tmp", "mcpServers": []}
        if harness == "claude-code":
            # repo_config ignore (connector.harnessFor)
            params["_meta"] = {"claudeCode": {"options": {"settingSources": ["user"]}}}
        new = call(2, "session/new", params)
        if not new.get("sessionId"):
            sys.exit(f"session/new: no session id: {new}")
        options = [o.get("id") for o in new.get("configOptions") or []]
        print("session/new:", new["sessionId"], "options:", options)
        if "model" not in options:
            sys.exit("session/new: no model option (the profile's model can't be applied)")
    finally:
        proc.kill()


if __name__ == "__main__":
    main()
