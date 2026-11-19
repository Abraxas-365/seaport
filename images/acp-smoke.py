#!/usr/bin/env python3
"""ACP smoke of a Seaport base image (CI, job images).

Starts the image's harness the way `seaport agent-run` does (hosted:
SEAPORT_SLOT, the gateway and the slot token as the Anthropic key; no
network, read-only root), then checks it answers initialize and opens a
session: what a slot needs before its first message. rness also runs as
the slots of other environments (RNESS_CASES: no gateway, OpenAI-protocol
providers through it, the agent's model, a Claude sign-in with or without
the gateway), each session starting on the model its
images/rness/seaport.lua picks.

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


GATEWAY = "http://127.0.0.1:9/gateway"


def slot(*providers, **env):
    """A slot's variables for an environment whose models name providers."""
    out = {"SEAPORT_GATEWAY": GATEWAY, "SEAPORT_GATEWAY_PROVIDERS": ",".join(providers)}
    for p in providers:
        out[p.upper() + "_BASE_URL"] = f"{GATEWAY}/{p}"
        out[p.upper() + "_API_KEY"] = "sp_crew_ci"
    return {**out, **env}


# (what, the slot's variables, the model its sessions must start on)
RNESS_CASES = [
    ("no gateway (key as an env secret)", {"ANTHROPIC_API_KEY": "sk-ci"}, "anthropic/claude-sonnet-5-5"),
    ("openrouter, the agent's model", slot("openrouter", SEAPORT_AGENT_MODEL="openrouter/acme/model-1"),
     "openrouter/acme/model-1"),
    ("openai, a bare agent model", slot("openai", SEAPORT_AGENT_MODEL="gpt-ci"), "openai/gpt-ci"),
    ("anthropic and deepseek, the agent's model", slot("anthropic", "deepseek", SEAPORT_AGENT_MODEL="deepseek/deepseek-chat"),
     "deepseek/deepseek-chat"),
    ("anthropic and groq, a bare Claude model", slot("anthropic", "groq", SEAPORT_AGENT_MODEL="claude-opus-5-5"),
     "anthropic/claude-sonnet-5-5"),
    ("signed in, no gateway", {"SEAPORT_SIGN_IN": "anthropic"}, "anthropic/claude-sonnet-5-5"),
    ("signed in to anthropic, groq through the gateway", slot("groq", SEAPORT_SIGN_IN="anthropic"),
     "anthropic/claude-sonnet-5-5"),
    ("signed in to anthropic, groq's model", slot("groq", SEAPORT_SIGN_IN="anthropic", SEAPORT_AGENT_MODEL="groq/llama-ci"),
     "groq/llama-ci"),
]


def main():
    image, harness = sys.argv[1], sys.argv[2]
    smoke(image, harness, slot("anthropic"), None)
    if harness == "rness":
        for what, env, model in RNESS_CASES:
            print(f"-- {what}")
            smoke(image, harness, env, model)


def smoke(image, harness, env, model):
    command = COMMANDS[harness]
    flags = [f for k, v in env.items() for f in ("-e", f"{k}={v}")]
    proc = subprocess.Popen(
        # the home as an anonymous volume, seeded from the image like the
        # slot's home volume
        ["docker", "run", "--rm", "-i", "--network", "none", "--read-only",
         "--tmpfs", "/tmp", "-v", "/home/agent",
         "-e", "SEAPORT_SLOT=ci-1",
         *flags,
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
        options = {o.get("id"): o for o in new.get("configOptions") or []}
        print("session/new:", new["sessionId"], "options:", list(options))
        if "model" not in options:
            sys.exit("session/new: no model option (the profile's model can't be applied)")
        current = options["model"].get("currentValue")
        if model and current != model:
            sys.exit(f"session/new: model {current}, want {model}")
    finally:
        proc.kill()


if __name__ == "__main__":
    main()
