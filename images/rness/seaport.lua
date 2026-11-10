-- Seaport base image: a hosted rness reaches models only through the port's
-- model gateway, with the slot's instance token as its key. Loaded first by
-- ~/.rness/init.lua (require("seaport")); a custom image that replaces
-- init.lua must keep that line.
if (os.getenv("SEAPORT_SLOT") or "") == "" then
  return -- not hosted (a local run of the image): the providers stay as declared
end

-- rness has no default model and `seaport agent-run` passes no -m: new
-- sessions start on this profile, and the agent's Seaport profile (model,
-- effort) is applied to each session afterwards. SEAPORT_MODEL overrides
-- the model (an Anthropic model the flavor declares).
rness.profiles.declare("seaport", {
  provider = "anthropic",
  model = os.getenv("SEAPORT_MODEL") or "claude-sonnet-5-5",
})
rness.default_profile = "seaport"

local gateway = os.getenv("SEAPORT_GATEWAY")
if gateway == nil or gateway == "" then
  return
end
gateway = gateway:gsub("/+$", "")

-- Providers the gateway serves: whatever the flavor declares for them, the
-- base URL becomes the gateway's and the key the instance token.
local routed = { anthropic = "/anthropic" }

local register = rness.providers.register
rness.providers.register = function(name, spec)
  local path = routed[name]
  if path then
    spec.base_url = gateway .. path
    spec.auth = { env = "ANTHROPIC_API_KEY" }
  end
  return register(name, spec)
end
