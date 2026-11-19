-- Seaport base image: a hosted rness reaches models only through the port's
-- model gateway, with the slot's instance token as its key. Loaded first by
-- ~/.rness/init.lua (require("seaport")); a custom image that replaces
-- init.lua must keep that line.
if (os.getenv("SEAPORT_SLOT") or "") == "" then
  return -- not hosted (a local run of the image): the providers stay as declared
end

-- A slot's only way out is the port's egress proxy (HTTPS_PROXY, with the
-- slot's credentials), and rness's web tools ignore proxy variables: hand
-- it to them as rness.web.proxy (always: no other way out exists), whenever
-- init.lua (the flavor's or a home's) assigns rness.web after this file ran.
local egress = os.getenv("HTTPS_PROXY") or ""
if egress ~= "" then
  local function proxied(web)
    if type(web) == "table" then
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

-- The providers the gateway can serve and their rness protocol (keep in
-- step with hosting.Providers; TestRnessGatewayProviders checks it).
local protocols = {
  anthropic = "anthropic",
  openai = "openai-chat",
  openrouter = "openai-chat",
  groq = "openai-chat",
  deepseek = "openai-chat",
}

-- The ones this slot's environment names (SEAPORT_GATEWAY_PROVIDERS,
-- sorted; an older port sets only Anthropic's variables) go through the
-- gateway. Anthropic may instead be reached outside it: the slot's sign-in
-- to the person's Claude subscription (SEAPORT_SIGN_IN, next to any of the
-- gateway's; or no gateway at all) or a key in an env secret
-- (ANTHROPIC_API_KEY), which wins over the sign-in.
local gateway = (os.getenv("SEAPORT_GATEWAY") or ""):gsub("/+$", "")
local served, routed, names = {}, {}, {}
local function serve(name)
  if protocols[name] and not served[name] then
    served[name] = true
    names[#names + 1] = name
  end
end
if gateway ~= "" then
  for name in (os.getenv("SEAPORT_GATEWAY_PROVIDERS") or "anthropic"):gmatch("[^,%s]+") do
    if protocols[name] then
      routed[name] = true
      serve(name)
    end
  end
  if #names == 0 then
    error("seaport: SEAPORT_GATEWAY_PROVIDERS names no provider this image knows")
  end
end
if not routed.anthropic and (gateway == "" or os.getenv("SEAPORT_SIGN_IN") == "anthropic") then
  serve("anthropic")
end
local signin = served.anthropic and not routed.anthropic and (os.getenv("ANTHROPIC_API_KEY") or "") == ""

-- Each routed provider is registered here, on the gateway (base URL
-- <gateway>/<provider>, key the instance token in <PROVIDER>_API_KEY), so
-- one the flavor doesn't declare (openai, groq, deepseek) exists too. A
-- later registration of the same name (the flavor's, a home's) is dropped:
-- the gateway's stands, and rness refuses a provider registered twice.
-- Signed in, the slot's sign-in (`seaport agents login`, the web's
-- "Conectar cuenta") is kept as rness's "anthropic" OAuth tokens; the
-- flavor (or a home) declares Anthropic with the key's variable, which
-- rness never swaps for a sign-in: such a registration uses it instead.
local register = rness.providers.register
for _, name in ipairs(names) do
  if routed[name] then
    register(name, {
      protocol = protocols[name],
      base_url = gateway .. "/" .. name,
      auth = { env = name:upper() .. "_API_KEY" },
    })
  end
end
if gateway ~= "" or signin then
  rness.providers.register = function(name, spec)
    if routed[name] then
      if name ~= "anthropic" and name ~= "openrouter" then -- the flavor's own
        rness.log.warn("seaport: provider " .. name .. " goes through the port's gateway; this registration is ignored")
      end
      return
    end
    if signin and name == "anthropic" and type(spec) == "table" and type(spec.auth) == "table"
      and spec.auth.env == "ANTHROPIC_API_KEY" then
      spec.auth = { oauth = "anthropic" }
    end
    return register(name, spec)
  end
end

-- rness has no default model and `seaport agent-run` passes no -m: new
-- sessions start on this profile. Its model is the first of
--   SEAPORT_AGENT_MODEL  the agent's profile model (agent-run sets it):
--                        <provider>/<model> of a served provider, or a
--                        bare model when the only one served isn't
--                        Anthropic (rness declares no models of the
--                        others, so they can't be picked among the
--                        session's options)
--   SEAPORT_MODEL        a custom image's (ENV): <provider>/<model>, or a
--                        bare model of the only provider, else Anthropic's
--   claude-sonnet-5-5    when Anthropic is served
-- A bare Anthropic model (claude-opus-5-5) is picked among the session's
-- options afterwards, with the effort.
local only = #names == 1 and names[1] or nil
local function selection(value, bare)
  value = value or ""
  local provider, model = value:match("^([^/]+)/(.+)$")
  if provider and served[provider] then
    return provider, model
  end
  if bare and value ~= "" and not provider then
    return bare, value
  end
end
local provider, model = selection(os.getenv("SEAPORT_AGENT_MODEL"), only ~= "anthropic" and only)
if not provider then
  provider, model = selection(os.getenv("SEAPORT_MODEL"), only or (served.anthropic and "anthropic"))
end
if not provider and served.anthropic then
  provider, model = "anthropic", "claude-sonnet-5-5"
end
if not provider then
  error("seaport: no model to start on: set the agent's model to <provider>/<model>, one of "
    .. table.concat(names, ", "))
end
rness.profiles.declare("seaport", { provider = provider, model = model })
rness.default_profile = "seaport"
