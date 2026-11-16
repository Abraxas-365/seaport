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

-- A slot's only way out is the port's egress proxy (HTTPS_PROXY, with the
-- slot's credentials), and rness's web tools ignore proxy variables: hand
-- it to them as rness.web.proxy, whenever init.lua (the flavor's or a
-- home's) assigns rness.web after this file ran.
local egress = os.getenv("HTTPS_PROXY") or ""
if egress ~= "" then
  local function proxied(web)
    if type(web) == "table" and web.proxy == nil then
      web.proxy = egress
    end
    return web
  end
  local web = proxied(rawget(rness, "web"))
  rawset(rness, "web", nil)
  setmetatable(rness, {
    __index = function(_, key)
      if key == "web" then
        return web
      end
    end,
    __newindex = function(t, key, value)
      if key == "web" then
        web = proxied(value)
      else
        rawset(t, key, value)
      end
    end,
  })
end

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
