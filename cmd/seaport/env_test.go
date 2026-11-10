package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/hosting"
)

const sampleEnv = `name: web
description: the web app
harness: claude-code
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

func TestEnvFileRoundTrip(t *testing.T) {
	f, err := readEnvFile([]byte(sampleEnv))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "web" || f.Harness != hosting.HarnessClaudeCode || f.Repos[0].Credential != "${GITHUB_TOKEN}" ||
		f.Setup != "npm i -g pnpm\npnpm --version\n" || f.Resources.Size != "M" ||
		f.Models.Anthropic != "${ANTHROPIC_API_KEY}" {
		t.Fatalf("read: %+v", f)
	}
	var out bytes.Buffer
	if err := writeEnvFile(&out, f); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "image:") || strings.Contains(out.String(), ": {") || strings.Contains(out.String(), "[") || strings.Contains(out.String(), "cpus") || !strings.Contains(out.String(), "setup: |") {
		t.Fatalf("export should be block YAML without empty keys:\n%s", out.String())
	}
	back, err := readEnvFile(out.Bytes())
	if err != nil {
		t.Fatalf("re-read export: %v\n%s", err, out.String())
	}
	if !reflect.DeepEqual(back, f) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", back, f)
	}
	for _, bad := range []string{"harness: rness\n", "name: x\nharnes: rness\n", "- a\n", "name: x\nrepos: {url: 1}\n"} {
		if _, err := readEnvFile([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
