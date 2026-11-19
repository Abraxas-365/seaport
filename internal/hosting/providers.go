package hosting

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Limits of a machine's plain variables (Machine.Env).
const (
	MaxEnv      = 50
	MaxEnvValue = 4096
)

// Subscription is a Machine.Models value: the provider is reached by
// signing the slot's harness in to the person's subscription (Anthropic
// only; SignsIn), not with a key.
const Subscription = "subscription"

// Protocol a provider speaks (Provider.Protocol): what the gateway
// forwards and how it meters.
const (
	// ProtocolAnthropic: the Messages API (x-api-key, usage in the
	// message or the stream's message_start/message_delta).
	ProtocolAnthropic = "anthropic"
	// ProtocolOpenAI: Chat Completions / Responses / embeddings
	// (Authorization: Bearer, usage.prompt_tokens/completion_tokens or
	// input_tokens/output_tokens; a stream's last chunk).
	ProtocolOpenAI = "openai"
)

// Provider is a model provider the gateway serves (Providers).
type Provider struct {
	// Name is the key of Machine.Models and the gateway's path segment.
	Name     string
	Protocol string
	// Upstream is the provider's API (the default; config overrides it).
	Upstream string
	// KeyEnv and BaseEnv are the variables the harness reads the provider's
	// key and base URL from: the slot gets BaseEnv=<gateway>/<name> and
	// KeyEnv=<slot token>.
	KeyEnv, BaseEnv string
}

// Providers the gateway serves (Machine.Models' keys). ProviderAnthropic is
// the one Claude Code uses; the others speak the OpenAI protocol and are
// the ones rness's default flavor declares with an env key.
var Providers = map[string]Provider{
	ProviderAnthropic: {Name: ProviderAnthropic, Protocol: ProtocolAnthropic, Upstream: "https://api.anthropic.com",
		KeyEnv: "ANTHROPIC_API_KEY", BaseEnv: "ANTHROPIC_BASE_URL"},
	"openai": {Name: "openai", Protocol: ProtocolOpenAI, Upstream: "https://api.openai.com/v1",
		KeyEnv: "OPENAI_API_KEY", BaseEnv: "OPENAI_BASE_URL"},
	"openrouter": {Name: "openrouter", Protocol: ProtocolOpenAI, Upstream: "https://openrouter.ai/api/v1",
		KeyEnv: "OPENROUTER_API_KEY", BaseEnv: "OPENROUTER_BASE_URL"},
	"groq": {Name: "groq", Protocol: ProtocolOpenAI, Upstream: "https://api.groq.com/openai/v1",
		KeyEnv: "GROQ_API_KEY", BaseEnv: "GROQ_BASE_URL"},
	"deepseek": {Name: "deepseek", Protocol: ProtocolOpenAI, Upstream: "https://api.deepseek.com/v1",
		KeyEnv: "DEEPSEEK_API_KEY", BaseEnv: "DEEPSEEK_BASE_URL"},
}

// ProviderNames are Providers' names, sorted.
func ProviderNames() []string {
	out := make([]string, 0, len(Providers))
	for n := range Providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// GatewayURL is where a slot calls provider through the gateway at base
// (the port's URL): base + GatewayPath + "/" + provider.
func GatewayURL(base, provider string) string {
	return strings.TrimRight(base, "/") + GatewayPath + "/" + provider
}

// SlotSet are the variables the port sets on a slot for its network and
// the agent's kit may not replace (connector.Config.Protect): the gateway,
// every provider's key and base URL, and the egress proxy's.
func SlotSet() []string {
	out := []string{EnvGateway, "HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "NO_PROXY", "no_proxy"}
	for _, n := range ProviderNames() {
		p := Providers[n]
		out = append(out, p.BaseEnv, p.KeyEnv)
	}
	return out
}

// ValidEnv checks Machine.Env: at most MaxEnv variables named like a secret
// (agent.ValidSecretName: A-Z, 0-9, _; not SEAPORT_), none of the slot's
// own (SlotSet), values one line of at most MaxEnvValue bytes that don't
// look like a credential (text.Secrets: those are secrets).
func ValidEnv(env map[string]string) error {
	if len(env) > MaxEnv {
		return errx.InvalidField("env", "at most "+strconv.Itoa(MaxEnv)+" variables")
	}
	slot := SlotSet()
	for k, v := range env {
		if err := agent.ValidSecretName(k); err != nil {
			return errx.InvalidField("env", "env "+strconv.Quote(k)+": name must be an environment variable name (A-Z, 0-9, _; not SEAPORT_…)")
		}
		if slices.Contains(slot, k) {
			return errx.InvalidField("env", "env "+k+" is set by the port for the slot's network")
		}
		if len(v) > MaxEnvValue || strings.ContainsAny(v, "\x00\n\r") {
			return errx.InvalidField("env", "env "+k+": value must be one line of at most "+strconv.Itoa(MaxEnvValue)+" bytes")
		}
		for _, s := range text.Secrets {
			if s.Re.MatchString(v) {
				return errx.InvalidField("env", "env "+k+" looks like "+s.What+": variables are stored in clear with the config; make it a secret of the agent (seaport agents secrets set … -env)")
			}
		}
	}
	return nil
}
