package hosting

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestRnessGatewayProviders: rness's base image routes through the gateway
// every provider the port can serve, with the protocol it speaks
// (images/rness/seaport.lua's table, kept by hand in Lua).
func TestRnessGatewayProviders(t *testing.T) {
	src, err := os.ReadFile("../../images/rness/seaport.lua")
	if err != nil {
		t.Fatal(err)
	}
	table := regexp.MustCompile(`(?s)local protocols = \{(.*?)\n\}`).FindSubmatch(src)
	if table == nil {
		t.Fatal("seaport.lua has no `local protocols = {…}` table")
	}
	rness := map[string]string{ProtocolAnthropic: "anthropic", ProtocolOpenAI: "openai-chat"}
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*(\w+) = "([^"]+)",`).FindAllSubmatch(table[1], -1) {
		got[string(m[1])] = string(m[2])
	}
	for name, p := range Providers {
		if got[name] != rness[p.Protocol] {
			t.Errorf("seaport.lua: %s = %q, want %q (%s protocol)", name, got[name], rness[p.Protocol], p.Protocol)
		}
		if p.KeyEnv != strings.ToUpper(name)+"_API_KEY" {
			t.Errorf("seaport.lua reads <PROVIDER>_API_KEY: %s's key is %s", name, p.KeyEnv)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if _, ok := Providers[name]; !ok {
			t.Errorf("seaport.lua routes %s, which the gateway doesn't serve", name)
		}
	}
}
