package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/hosting"
)

const sampleConfig = `harness: claude-code
instructions: |
  Be brief.
  Answer in Spanish.
model: sonnet
machine:
  packages: [jq, ripgrep]
  setup: |
    npm i -g pnpm
    pnpm --version
  repos:
    - url: https://github.com/acme/app.git
      ref: main
      credential: ${GITHUB_TOKEN}
      on_start: pnpm i
  models:
    anthropic: ${ANTHROPIC_API_KEY}
  network:
    egress: [registry.npmjs.org]
  resources: {size: M}
  idle_stop: 2h
`

func TestConfigFileRoundTrip(t *testing.T) {
	c, err := readConfigFile([]byte(sampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Machine
	if c.Harness != hosting.HarnessClaudeCode || c.Model != "sonnet" || m == nil || m.Repos[0].Credential != "${GITHUB_TOKEN}" ||
		m.Setup != "npm i -g pnpm\npnpm --version\n" || m.Resources.Size != "M" ||
		m.Models["anthropic"] != "${ANTHROPIC_API_KEY}" || c.Instructions != "Be brief.\nAnswer in Spanish.\n" {
		t.Fatalf("read: %+v %+v", c, m)
	}
	var out bytes.Buffer
	if err := writeConfigFile(&out, c); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Contains(s, "image:") || strings.Contains(s, ": {") || strings.Contains(s, "[") || strings.Contains(s, "cpus") ||
		!strings.Contains(s, "setup: |") || !strings.Contains(s, "instructions: |") || strings.Contains(s, "null") {
		t.Fatalf("show should be block YAML without empty keys:\n%s", s)
	}
	back, err := readConfigFile(out.Bytes())
	if err != nil {
		t.Fatalf("re-read show: %v\n%s", err, s)
	}
	if back.Skills == nil { // empty lists are pruned on the way out
		back.Skills, back.MCPServers, back.BrowserIgnoreHosts = c.Skills, c.MCPServers, c.BrowserIgnoreHosts
	}
	if !reflect.DeepEqual(back, c) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", back, c)
	}
	for _, bad := range []string{"harnes: rness\n", "- a\n", "machine: {repos: {url: 1}}\n", "machine: {imag: x}\n"} {
		if _, err := readConfigFile([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// no machine: none is written (and none read back)
	plain, err := readConfigFile([]byte("instructions: hi\n"))
	if err != nil || plain.Machine != nil {
		t.Fatalf("plain: %v %+v", err, plain)
	}
	out.Reset()
	if err := writeConfigFile(&out, plain); err != nil || strings.Contains(out.String(), "machine") {
		t.Fatalf("plain show: %v\n%s", err, out.String())
	}
	// "*" (any public host) must survive a show: unquoted it is a YAML alias.
	open, err := readConfigFile([]byte("harness: rness\nmachine:\n  network:\n    egress: [\"*\", \"*.example.com\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := writeConfigFile(&out, open); err != nil {
		t.Fatal(err)
	}
	if back, err := readConfigFile(out.Bytes()); err != nil || !reflect.DeepEqual(back.Machine.Network.Egress, []string{"*", "*.example.com"}) {
		t.Fatalf("egress * round trip: %v\n%s", err, out.String())
	}
}
