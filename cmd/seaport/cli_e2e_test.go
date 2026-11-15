package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/query"
)

const cliAdminToken = "cli-admin-token-0123456789"

// cliPort is a real port (the server `seaport serve` runs, on a random
// port and a wiped test database) the CLI tests talk to through run.
type cliPort struct {
	t   *testing.T
	url string
}

// testDatabaseURL is the throwaway database of the integration tests.
// Tests skip without it, except in CI, where a missing database must fail.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("SEAPORT_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("SEAPORT_TEST_DATABASE_URL not set in CI")
		}
		t.Skip("SEAPORT_TEST_DATABASE_URL not set")
	}
	return url
}

// startCLIPort wipes the test database and starts a port on it, with the
// secret key and blob store the secrets and harness homes need.
func startCLIPort(t *testing.T) *cliPort {
	t.Helper()
	dsn := testDatabaseURL(t)
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	cfg := config.Server{
		Addr: addr, DatabaseURL: dsn, PublicURL: "http://" + addr, AdminToken: cliAdminToken,
		SecretKey: "cli-e2e-secrets-key-0123456789abcdefXYZ",
		BlobURL:   config.LocalBlobURL(filepath.Join(t.TempDir(), "blobs")),
	}
	app, err := bootstrap.Build(ctx, cfg, bootstrap.Options{DisableWhatsApp: true, Lease: 2 * time.Second, DockLease: 3 * time.Second})
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	app.Start(runCtx)
	served := make(chan error, 1)
	go func() { served <- app.ServeListener(runCtx, ln) }()
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			cancel()
			<-served
			app.Close()
		})
	})
	return &cliPort{t: t, url: "http://" + addr}
}

// cli runs `seaport args...` against the port as the admin, with in as
// stdin, and answers what it printed to stdout and stderr and its error.
func (p *cliPort) cli(in string, args ...string) (string, string, error) {
	p.t.Helper()
	return p.cliAs(cliAdminToken, in, args...)
}

func (p *cliPort) cliAs(token, in string, args ...string) (string, string, error) {
	p.t.Helper()
	p.t.Setenv("SEAPORT_URL", p.url)
	p.t.Setenv("SEAPORT_ADMIN_TOKEN", token)
	var out, errOut bytes.Buffer
	oldIn, oldOut, oldErr := stdin, stdout, stderr
	stdin, stdout, stderr = strings.NewReader(in), &out, &errOut
	defer func() { stdin, stdout, stderr = oldIn, oldOut, oldErr }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := run(ctx, args)
	return out.String(), errOut.String(), err
}

// ok runs a command that must succeed and answers its stdout.
func (p *cliPort) ok(args ...string) string {
	p.t.Helper()
	out, _, err := p.cli("", args...)
	if err != nil {
		p.t.Fatalf("seaport %s: %v (stdout %q)", strings.Join(args, " "), err, out)
	}
	return out
}

// fails runs a command that must fail and answers the message the CLI
// would print after "seaport:".
func (p *cliPort) fails(args ...string) string {
	p.t.Helper()
	out, _, err := p.cli("", args...)
	if err == nil {
		p.t.Fatalf("seaport %s succeeded: %q", strings.Join(args, " "), out)
	}
	return errorMessage(err)
}

// api calls the HTTP API as the admin.
func (p *cliPort) api(method, path string, body, out any) int {
	p.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, p.url+path, rd)
	req.Header.Set("Authorization", "Bearer "+cliAdminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// firstID is the first UUID printed in s.
func firstID(t *testing.T, s string) string {
	t.Helper()
	id := uuidRE.FindString(s)
	if id == "" {
		t.Fatalf("no id in %q", s)
	}
	return id
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s: missing %q in:\n%s", what, w, got)
		}
	}
}

// addDock creates a dock through the CLI and answers its id.
func (p *cliPort) addDock(channel, name string) string {
	p.t.Helper()
	out := p.ok("dock", "add", channel, "-name", name)
	mustContain(p.t, "dock add "+channel, out, "created ("+channel+")")
	return firstID(p.t, out)
}

// inject delivers an inbound message to an api dock and answers the
// conversation it opened.
func (p *cliPort) inject(dockID, chat, msgID, text string) string {
	p.t.Helper()
	var s struct {
		Conversation string `json:"conversation_id"`
	}
	if code := p.api(http.MethodPost, "/api/v1/docks/"+dockID+"/ships",
		map[string]any{"chat_id": chat, "chat_title": "Chat " + chat, "message_id": msgID, "sender_id": chat, "sender_name": "Ana", "text": text}, &s); code != http.StatusCreated {
		p.t.Fatalf("inject: %d", code)
	}
	if s.Conversation == "" {
		p.t.Fatalf("inject: no conversation in the answer")
	}
	return s.Conversation
}

// The dock commands: create, list, rename, replies on/off (refused on a
// channel that can't reply), destinations, open, unlink and delete.
func TestCLIDocks(t *testing.T) {
	p := startCLIPort(t)

	apiDock := p.addDock("api", "support")
	hook := p.addDock("webhook", "alerts")

	list := p.ok("dock", "list")
	mustContain(t, "dock list", list, "ID", "REPLIES", apiDock, "support", "alerts", "webhook")

	mustContain(t, "rename", p.ok("dock", "rename", apiDock, "customer", "care"), `is now "customer care"`)
	mustContain(t, "list after rename", p.ok("dock", "list"), "customer care")

	// replies off, then on, as the API sees it
	mustContain(t, "replies off", p.ok("dock", "replies", apiDock, "off"), "is a trigger now")
	var d dock.Dock
	if code := p.api(http.MethodGet, "/api/v1/docks/"+apiDock, nil, &d); code != http.StatusOK || d.Replies {
		t.Fatalf("dock after replies off: %d %+v", code, d)
	}
	if got := p.ok("dock", "list"); !regexp.MustCompile(`customer care\s+\S+\s+off`).MatchString(got) {
		t.Errorf("dock list does not show replies off:\n%s", got)
	}
	mustContain(t, "replies on", p.ok("dock", "replies", apiDock, "on"), "takes replies again")
	_ = p.api(http.MethodGet, "/api/v1/docks/"+apiDock, nil, &d)
	if !d.Replies {
		t.Fatalf("replies still off: %+v", d)
	}

	// the webhook channel can't reply: the switch itself is refused
	if msg := p.fails("dock", "replies", hook, "on"); msg == "" {
		t.Error("empty refusal")
	}
	if msg := p.fails("dock", "replies", apiDock, "maybe"); !strings.Contains(msg, "usage: seaport dock replies") {
		t.Errorf("bad switch: %q", msg)
	}

	// destinations
	mustContain(t, "no destinations", p.ok("dock", "destinations", apiDock), "no destinations")
	mustContain(t, "destination set", p.ok("dock", "destination", "set", apiDock, "ops", "ops-room", "-title", "Operations"),
		"destination ops of dock "+apiDock, "ops-room (api)")
	mustContain(t, "destinations", p.ok("dock", "destinations", apiDock), "ops", "api", "ops-room", "(Operations)")
	if msg := p.fails("dock", "destination", "set", apiDock, "Bad Name!", "x"); msg == "" {
		t.Error("an invalid destination name was accepted")
	}
	if msg := p.fails("dock", "destination", "set", apiDock); !strings.Contains(msg, "usage: seaport dock destination") {
		t.Errorf("destination usage: %q", msg)
	}

	// open: the first message at the destination makes the conversation
	out := p.ok("dock", "open", apiDock, "ops", "deploy", "finished")
	mustContain(t, "dock open", out, "opened conversation ", "ship ", "sent")
	conv := firstID(t, out)
	var c conversation.Conversation
	if code := p.api(http.MethodGet, "/api/v1/conversations/"+conv, nil, &c); code != http.StatusOK || c.ID.String() != conv || c.Access != conversation.AccessAllow {
		t.Fatalf("opened conversation: %d %+v", code, c)
	}
	mustContain(t, "history of the opened chat", p.ok("history", conv), "agent: deploy finished")
	if msg := p.fails("dock", "open", apiDock, "nope", "hello"); !strings.Contains(strings.ToLower(msg), "destination") {
		t.Errorf("unknown destination: %q", msg)
	}
	if msg := p.fails("dock", "open", apiDock, "ops"); !strings.Contains(msg, "usage: seaport dock open") {
		t.Errorf("open usage: %q", msg)
	}

	mustContain(t, "destination rm", p.ok("dock", "destination", "rm", apiDock, "ops"), "destination ops removed")
	mustContain(t, "no destinations again", p.ok("dock", "destinations", apiDock), "no destinations")

	// an api dock has no link to cut; a webhook dock unlinks (its URL stops working)
	if msg := p.fails("dock", "unlink", apiDock); !strings.Contains(msg, "api docks have no link") {
		t.Errorf("unlink of an api dock: %q", msg)
	}
	mustContain(t, "unlink", p.ok("dock", "unlink", hook), "dock "+hook+" unlinked")
	// delete needs -yes and removes the dock
	if msg := p.fails("dock", "delete", apiDock); !strings.Contains(msg, "usage: seaport dock delete") {
		t.Errorf("delete without -yes: %q", msg)
	}
	mustContain(t, "delete", p.ok("dock", "delete", apiDock, "-yes"), "dock deleted")
	if strings.Contains(p.ok("dock", "list"), apiDock) {
		t.Error("the deleted dock is still listed")
	}
	if code := p.api(http.MethodGet, "/api/v1/docks/"+apiDock, nil, nil); code != http.StatusNotFound {
		t.Fatalf("deleted dock: %d", code)
	}
}

// The webhook and Datadog docks: info, rotate and templates.
func TestCLIWebhookAndDatadog(t *testing.T) {
	p := startCLIPort(t)
	hook := p.addDock("webhook", "alerts")
	dd := p.addDock("datadog", "monitor")
	api := p.addDock("api", "chat")

	info := p.ok("dock", "webhook", hook)
	mustContain(t, "webhook info", info, "url:", "/hooks/"+hook, "template: (default")
	if strings.Contains(info, "secret:") {
		t.Error("a secret was shown without -rotate")
	}
	rot := p.ok("dock", "webhook", hook, "-rotate")
	mustContain(t, "webhook rotate", rot, "secret:", "(shown once")

	dir := t.TempDir()
	tpl := filepath.Join(dir, "template.json")
	sample := filepath.Join(dir, "sample.json")
	bad := filepath.Join(dir, "bad.json")
	for path, body := range map[string]string{
		tpl:    `{"chat_id": "{{.repo}}", "text": "{{.msg}}"}`,
		sample: `{"repo": "acme/app", "msg": "hello"}`,
		bad:    `{not json`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saved := p.ok("dock", "webhook", hook, "-template", tpl, "-sample", sample)
	mustContain(t, "template saved", saved, "template saved", "preview:", "acme/app", "hello")
	mustContain(t, "template kept", p.ok("dock", "webhook", hook), "template: {", "chat_id")
	if msg := p.fails("dock", "webhook", hook, "-template", bad); !strings.Contains(msg, "is not valid JSON") {
		t.Errorf("bad template: %q", msg)
	}
	if msg := p.fails("dock", "webhook", hook, "-sample", sample); !strings.Contains(msg, "-sample needs -template") {
		t.Errorf("sample without template: %q", msg)
	}
	if msg := p.fails("dock", "webhook"); !strings.Contains(msg, "usage: seaport dock webhook") {
		t.Errorf("webhook usage: %q", msg)
	}

	// Datadog: a mirror names a destination of another dock
	mustContain(t, "datadog info", p.ok("dock", "datadog", dd), "/datadog/webhook/"+dd)
	mustContain(t, "datadog rotate", p.ok("dock", "datadog", dd, "-rotate"), "secret:")
	p.ok("dock", "destination", "set", api, "alerts-room", "alerts")
	mustContain(t, "mirror", p.ok("dock", "datadog", dd, "-mirror", api+":alerts-room"), "mirror saved", api)
	mustContain(t, "no mirror", p.ok("dock", "datadog", dd, "-no-mirror"), "mirror saved")
	if msg := p.fails("dock", "datadog", dd, "-mirror", "nodestination"); !strings.Contains(msg, "-mirror must be") {
		t.Errorf("bad mirror: %q", msg)
	}
	if msg := p.fails("dock", "datadog", dd, "-mirror", "a:b", "-no-mirror"); !strings.Contains(msg, "exclude each other") {
		t.Errorf("both mirror flags: %q", msg)
	}
	if msg := p.fails("dock", "email", api); !strings.Contains(msg, "-address is required") {
		t.Errorf("email without address: %q", msg)
	}
	if msg := p.fails("dock", "email"); !strings.Contains(msg, "usage: seaport dock email") {
		t.Errorf("email usage: %q", msg)
	}
}

// The customs commands and conversation overrides, on messages injected
// through the API.
func TestCLICustomsAndConversations(t *testing.T) {
	p := startCLIPort(t)
	d := p.addDock("api", "support")

	mustContain(t, "customs", p.ok("customs"), "default: ask", "no rules")
	// a chat that arrives while the dock asks is pending
	conv := p.inject(d, "chat-1", "m1", "hello?")
	mustContain(t, "pending", p.ok("conversations", "-pending"), conv, "pending")
	mustContain(t, "allowed list is empty", p.ok("conversations", "-allowed"), "ID")
	if strings.Contains(p.ok("conversations", "-allowed"), conv) {
		t.Error("a pending chat is listed as allowed")
	}

	mustContain(t, "allow", p.ok("allow", conv), conv, "access=allow")
	mustContain(t, "allowed", p.ok("conversations", "-allowed"), conv)
	mustContain(t, "deny", p.ok("deny", conv), "access=deny")
	mustContain(t, "denied", p.ok("convs", "-denied"), conv)
	mustContain(t, "auto", p.ok("auto", conv), "access=auto")
	if msg := p.fails("allow"); !strings.Contains(msg, "usage: seaport allow|deny|auto") {
		t.Errorf("allow usage: %q", msg)
	}
	if msg := p.fails("mode", conv, "all"); msg == "" {
		t.Error("a group mode on a direct chat was accepted")
	}
	if msg := p.fails("mode", conv); !strings.Contains(msg, "usage: seaport mode") {
		t.Errorf("mode usage: %q", msg)
	}

	// rules and policy
	out, errOut, err := p.cli("", "customs", "default", "allow")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "default allow", out, "default: allow")
	mustContain(t, "warning", errOut, "warning: every conversation")
	mustContain(t, "rule", p.ok("customs", "deny", "chat-9"), "RULE", "deny", "chat-9")
	mustContain(t, "allow rule", p.ok("customs", "allow", "chat-*"), "allow", "chat-*")
	mustContain(t, "groups", p.ok("customs", "groups", "all"), "groups: all")
	var cu conversation.Customs
	if code := p.api(http.MethodGet, "/api/v1/docks/"+d+"/customs", nil, &cu); code != http.StatusOK || len(cu.Rules) != 2 {
		t.Fatalf("customs over the API: %d %+v", code, cu)
	}
	p.ok("customs", "rm", cu.Rules[0].ID.String())
	if code := p.api(http.MethodGet, "/api/v1/docks/"+d+"/customs", nil, &cu); code != http.StatusOK || len(cu.Rules) != 1 {
		t.Fatalf("customs after rm: %d %+v", code, cu)
	}
	if msg := p.fails("customs", "default", "sometimes"); msg == "" {
		t.Error("an invalid default was accepted")
	}
	if msg := p.fails("customs", "frobnicate"); !strings.Contains(msg, "usage: seaport customs") {
		t.Errorf("customs usage: %q", msg)
	}

	// -dock is required with several docks
	p.addDock("api", "second")
	if msg := p.fails("customs"); !strings.Contains(msg, "several docks") {
		t.Errorf("several docks: %q", msg)
	}
	mustContain(t, "customs -dock", p.ok("customs", "-dock", d), "dock "+d)
}

// Agents: the library, teams, profiles and secrets.
func TestCLIAgents(t *testing.T) {
	p := startCLIPort(t)
	d := p.addDock("api", "support")

	mustContain(t, "no agents", p.ok("agents"), "no agents yet")
	out := p.ok("agents", "add", "triage", "-description", "reads the queue")
	mustContain(t, "agents add", out, "agent triage created")
	agentID := firstID(t, out)
	p.ok("agent", "add", "writer")
	if msg := p.fails("agents", "add", "triage"); msg == "" {
		t.Error("a duplicate agent name was accepted")
	}

	list := p.ok("agents", "list")
	mustContain(t, "agents list", list, "NAME", agentID, "triage", "writer", "- (inactive)", "reads the queue")

	mustContain(t, "assign", p.ok("agents", "assign", "triage", d, "-default"), "agent triage works dock "+d)
	p.ok("agents", "assign", "@writer", d)
	list = p.ok("agents")
	mustContain(t, "agents after assign", list, d+" (default)")

	mustContain(t, "edit", p.ok("agents", "edit", "writer", "-description", "drafts replies"), "agent writer updated")
	mustContain(t, "agents after edit", p.ok("agents"), "drafts replies")
	if msg := p.fails("agents", "edit", "writer"); !strings.Contains(msg, "nothing to change") {
		t.Errorf("edit without flags: %q", msg)
	}
	if msg := p.fails("agents", "edit", "ghost", "-name", "x"); !strings.Contains(msg, "no agent ghost") {
		t.Errorf("unknown agent: %q", msg)
	}

	mustContain(t, "unassign", p.ok("agents", "unassign", "writer", d), "agent writer no longer works dock "+d)

	// profile: none yet, then instructions, then more
	mustContain(t, "no profile", p.ok("agents", "profile", "triage"), "has no profile yet")
	mustContain(t, "no versions", p.ok("agents", "profile", "versions", "triage"), "has no profile yet")
	dir := t.TempDir()
	instr := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(instr, []byte("Be brief.\nAnswer in Spanish.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "profile set", p.ok("agents", "profile", "set", "triage", "-instructions-file", instr, "-approve", "allow", "-model", "sonnet"),
		"agent triage profile v1 saved")
	show := p.ok("agents", "profile", "show", "triage")
	mustContain(t, "profile show", show, "profile v1", "approve: allow", "model: sonnet", "browser: off", "  Be brief.", "  Answer in Spanish.", "skills (0)")

	skillDir := filepath.Join(dir, "skills", "triage")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: bug-triage\ndescription: When a customer reports a bug\n---\nAsk for steps.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "skills", p.ok("agents", "profile", "set", "triage", "-skills-dir", filepath.Join(dir, "skills"), "-effort", "high"),
		"profile v2 saved")
	mustContain(t, "skill shown", p.ok("agents", "profile", "triage"), "skills (1)", "bug-triage: When a customer reports a bug", "effort: high")
	mustContain(t, "versions", p.ok("agents", "profile", "versions", "triage"), "2 (current)", "1")
	mustContain(t, "old version", p.ok("agents", "profile", "show", "triage", "-version", "1"), "profile v1", "skills (0)")

	js := p.ok("agents", "profile", "show", "triage", "-json")
	var pf profileFile
	if err := json.Unmarshal([]byte(js), &pf); err != nil || pf.Model != "sonnet" || len(pf.Skills) != 1 || !strings.Contains(pf.Instructions, "Be brief.") {
		t.Fatalf("profile -json: %v %+v", err, pf)
	}
	// the JSON round-trips through set -f; clearing a field with "none"
	file := filepath.Join(dir, "profile.json")
	if err := os.WriteFile(file, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "set -f", p.ok("agents", "profile", "set", "triage", "-f", file, "-model", "none"), "profile v3 saved")
	mustContain(t, "model cleared", p.ok("agents", "profile", "show", "triage"), "model: harness default")
	mustContain(t, "rm skill", p.ok("agents", "profile", "set", "triage", "-rm-skill", "bug-triage"), "profile v4 saved")
	if msg := p.fails("agents", "profile", "set", "triage", "-rm-skill", "nope"); !strings.Contains(msg, "has no skill nope") {
		t.Errorf("rm unknown skill: %q", msg)
	}
	if msg := p.fails("agents", "profile", "set", "triage", "-approve", "sometimes"); !strings.Contains(msg, "-approve must be") {
		t.Errorf("bad approve: %q", msg)
	}
	if msg := p.fails("agents", "profile", "set", "triage"); !strings.Contains(msg, "nothing to change") {
		t.Errorf("set without flags: %q", msg)
	}
	if msg := p.fails("agents", "profile", "set", "triage", "-browser", "maybe"); !strings.Contains(msg, "-browser must be on or off") {
		t.Errorf("bad browser: %q", msg)
	}
	// the switch is only a flag of the profile: the port doesn't check hosting here
	mustContain(t, "browser on", p.ok("agents", "profile", "set", "triage", "-browser", "on"), "profile v5 saved")
	mustContain(t, "browser shown", p.ok("agents", "profile", "triage"), "browser: on")

	// secrets: values come from stdin and are never shown
	mustContain(t, "no secrets", p.ok("agents", "secrets", "triage"), "has no secrets")
	out, _, err := p.cli("s3cr3t-value\n", "agents", "secrets", "set", "triage", "API_TOKEN", "-env")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "secret set", out, "secret API_TOKEN of agent triage saved", "${API_TOKEN}")
	if strings.Contains(out, "s3cr3t-value") {
		t.Error("the secret's value was printed")
	}
	sl := p.ok("agents", "secrets", "list", "triage")
	mustContain(t, "secrets list", sl, "NAME", "API_TOKEN", "true")
	if strings.Contains(sl, "s3cr3t-value") {
		t.Error("the secret's value was listed")
	}

	// an MCP header that uses it pins it
	cfg := filepath.Join(dir, "mcp.json")
	body := `{"mcp_servers":[{"name":"tools","url":"https://mcp.example.com/mcp","headers":[{"name":"Authorization","value":"Bearer ${API_TOKEN}"}]}]}`
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "mcp profile", p.ok("agents", "profile", "set", "triage", "-f", cfg), "profile v6 saved")
	mustContain(t, "mcp shown", p.ok("agents", "profile", "triage"), "MCP servers (1)", "tools", "https://mcp.example.com/mcp", "Authorization: Bearer ${API_TOKEN}")
	if msg := p.fails("agents", "secrets", "rm", "triage", "API_TOKEN"); msg == "" {
		t.Error("a secret an MCP header uses was deleted")
	}
	// a header may name only secrets the agent has
	bad := filepath.Join(dir, "mcp-bad.json")
	if err := os.WriteFile(bad, []byte(strings.ReplaceAll(body, "API_TOKEN", "OTHER")), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := p.fails("agents", "profile", "set", "triage", "-f", bad); msg == "" {
		t.Error("a header naming a missing secret was accepted")
	}
	// an unknown key in the file is an error, not a silently dropped setting
	typo := filepath.Join(dir, "typo.json")
	if err := os.WriteFile(typo, []byte(`{"instruction":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := p.fails("agents", "profile", "set", "triage", "-f", typo); !strings.Contains(msg, "profile file:") {
		t.Errorf("unknown key: %q", msg)
	}
	p.ok("agents", "profile", "set", "triage", "-f", file) // no header uses it any more
	mustContain(t, "secret rm", p.ok("agents", "secrets", "rm", "triage", "API_TOKEN"), "secret API_TOKEN of agent triage deleted")
	mustContain(t, "no secrets again", p.ok("agents", "secrets", "list", "triage"), "has no secrets")
	if msg := p.fails("agents", "secrets", "set", "triage"); !strings.Contains(msg, "usage: seaport agents secrets set") {
		t.Errorf("secret usage: %q", msg)
	}

	// delete needs -yes
	if msg := p.fails("agents", "delete", "writer"); !strings.Contains(msg, "add -yes") {
		t.Errorf("delete without -yes: %q", msg)
	}
	mustContain(t, "delete", p.ok("agents", "delete", "writer", "-yes"), "agent writer deleted")
	if strings.Contains(p.ok("agents"), "writer") {
		t.Error("the deleted agent is still listed")
	}
	if msg := p.fails("agents", "frobnicate"); !strings.Contains(msg, "unknown command agents frobnicate") {
		t.Errorf("unknown agents command: %q", msg)
	}
}

// Linked conversations: thread link/unlink, history and history -thread.
func TestCLIThreadsAndHistory(t *testing.T) {
	p := startCLIPort(t)
	d := p.addDock("api", "support")
	p.ok("customs", "default", "allow")

	a := p.inject(d, "chat-a", "a1", "first from A")
	b := p.inject(d, "chat-b", "b1", "first from B")
	p.inject(d, "chat-a", "a2", "second from A")

	hist := p.ok("history", a)
	mustContain(t, "history", hist, "Ana: first from A", "Ana: second from A")
	if strings.Contains(hist, "from B") {
		t.Errorf("history of A shows B:\n%s", hist)
	}
	if strings.Index(hist, "first from A") > strings.Index(hist, "second from A") {
		t.Errorf("history is not oldest first:\n%s", hist)
	}
	mustContain(t, "history -limit", p.ok("history", a, "-limit", "1"), "second from A")
	if strings.Contains(p.ok("history", a, "-limit", "1"), "first from A") {
		t.Error("-limit 1 shows two messages")
	}

	// alone, a thread is the conversation itself
	mustContain(t, "thread of one", p.ok("thread", a), a, "api", "Chat chat-a")

	linked := p.ok("thread", "link", a, b)
	mustContain(t, "thread link", linked, "CHANNEL", a, b)
	mustContain(t, "thread", p.ok("threads", b), a, b)

	whole := p.ok("history", a, "-thread")
	mustContain(t, "history -thread", whole, a, b, "Ana: first from A", "Ana: first from B", "Ana: second from A")

	out := p.ok("thread", "unlink", b)
	mustContain(t, "thread unlink", out, b, "is on its own again")
	if strings.Contains(p.ok("thread", a), b) {
		t.Error("the unlinked door is still in the thread")
	}

	if msg := p.fails("thread", "link", a); !strings.Contains(msg, "usage: seaport thread") {
		t.Errorf("link usage: %q", msg)
	}
	if msg := p.fails("thread"); !strings.Contains(msg, "usage: seaport thread") {
		t.Errorf("thread usage: %q", msg)
	}
	if msg := p.fails("thread", "link", a, a); msg == "" {
		t.Error("a conversation was linked to itself")
	}
	if msg := p.fails("history"); !strings.Contains(msg, "usage: seaport history") {
		t.Errorf("history usage: %q", msg)
	}

	// send posts as the port into an allowed conversation
	sent := p.ok("send", a, "thanks", "for", "waiting")
	mustContain(t, "send", sent, "ship ")
	mustContain(t, "sent in history", p.ok("history", a), "agent: thanks for waiting")
	if msg := p.fails("send", a); !strings.Contains(msg, "usage: seaport send") {
		t.Errorf("send usage: %q", msg)
	}
}

// The agent-side commands that talk to the port as a crew token: reply,
// reply -file, ask and approval.
func TestCLIAgentCommands(t *testing.T) {
	p := startCLIPort(t)
	d := p.addDock("api", "support")
	p.ok("agents", "add", "ci")
	p.ok("agents", "assign", "ci", d, "-default")
	p.ok("customs", "default", "allow")
	conv := p.inject(d, "chat-1", "m1", "deploy?")

	// crew tokens: created once, listed by hint, revoked
	if msg := p.fails("crew", "tokens", "create", "t1"); !strings.Contains(msg, "every crew token belongs to an agent") {
		t.Errorf("token without agent: %q", msg)
	}
	out, errOut, err := p.cli("", "crew", "tokens", "create", "runner", "-agent", "ci", "-expires", "720h")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(out)
	if !strings.HasPrefix(token, "sp_crew_") {
		t.Fatalf("token on stdout = %q", out)
	}
	mustContain(t, "token note", errOut, "for agent ci created", "shown only once")
	list := p.ok("crew", "tokens")
	mustContain(t, "tokens list", list, "AGENT", "ci", "runner", token[:8])
	if strings.Contains(list, token) {
		t.Error("the whole token is listed")
	}
	tokenID := firstID(t, errOut)
	mustContain(t, "crew status", p.ok("crew"), "no agents connected")

	// reply and ask use SEAPORT_URL and SEAPORT_CREW_TOKEN
	if _, _, err := p.cli("", "reply", conv, "no", "token"); err == nil || !strings.Contains(errorMessage(err), "SEAPORT_CREW_TOKEN") {
		t.Errorf("reply without a token: %v", err)
	}
	t.Setenv("SEAPORT_CREW_TOKEN", token)
	out = p.ok("reply", conv, "deploying", "now")
	if !regexp.MustCompile(`^` + uuidRE.String() + ` (sent|pending)\n$`).MatchString(out) {
		t.Errorf("reply printed %q", out)
	}
	mustContain(t, "reply in history", p.ok("history", conv), "agent: deploying now")

	file := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(file, []byte("# QA\nall green\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = p.ok("reply", conv, "-file", file, "report", "attached")
	if !regexp.MustCompile(uuidRE.String() + ` (sent|pending)`).MatchString(out) {
		t.Errorf("reply -file printed %q", out)
	}
	shipID := firstID(t, out)
	var hist struct {
		Items []struct {
			ID          string `json:"id"`
			Attachments []struct {
				Name string `json:"name"`
				Kind string `json:"kind"`
			} `json:"attachments"`
		} `json:"items"`
	}
	if code := p.api(http.MethodGet, "/api/v1/conversations/"+conv+"/ships", nil, &hist); code != http.StatusOK {
		t.Fatalf("history: %d", code)
	}
	found := false
	for _, s := range hist.Items {
		if s.ID == shipID {
			found = len(s.Attachments) == 1 && s.Attachments[0].Name == "report.md" && s.Attachments[0].Kind == "document"
		}
	}
	if !found {
		t.Errorf("the reply's file is not stored: %+v", hist.Items)
	}
	if _, _, err := p.cli("", "reply", conv, "-file", filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Error("a missing file was accepted")
	}
	if msg := p.fails("reply", "not-an-id", "x"); !strings.Contains(msg, "usage: seaport reply") {
		t.Errorf("reply usage: %q", msg)
	}
	if msg := p.fails("reply"); !strings.Contains(msg, "usage: seaport reply") {
		t.Errorf("reply no args: %q", msg)
	}

	// ask: a card, answered through the API; approval reads it back
	out = p.ok("ask", conv, "-option", "Approve", "-option", "Reject", "Deploy", "v2?")
	if !regexp.MustCompile(`^` + uuidRE.String() + ` pending\n$`).MatchString(out) {
		t.Fatalf("ask printed %q", out)
	}
	approval := firstID(t, out)
	if msg := p.fails("ask", conv, "-option", "Only", "one?"); !strings.Contains(msg, "option") {
		t.Errorf("ask with one option: %q", msg)
	}
	if msg := p.fails("ask", conv, "-option", "A", "-option", "B", "again?"); msg == "" {
		t.Error("a second pending approval was accepted")
	}
	mustContain(t, "approval pending", p.ok("approval", approval), approval, "pending")
	if code := p.api(http.MethodPost, "/api/v1/approvals/"+approval+"/answer", map[string]string{"option": "Approve"}, nil); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	mustContain(t, "approval answered", p.ok("approval", approval, "-wait", "1"), approval, "answered", "Approve")
	if msg := p.fails("approval"); !strings.Contains(msg, "usage: seaport approval") {
		t.Errorf("approval usage: %q", msg)
	}
	if msg := p.fails("ask", conv, "-wait", "x", "-option", "A", "-option", "B", "q"); !strings.Contains(msg, "-wait is a number of seconds") {
		t.Errorf("ask bad wait: %q", msg)
	}

	// a revoked token stops working
	mustContain(t, "revoke", p.ok("crew", "tokens", "revoke", tokenID), "crew token revoked")
	if msg := p.fails("reply", conv, "again"); msg == "" {
		t.Error("a revoked token replied")
	}
}

// Schedules: create (with a zone), list, preview, edit, enable/disable,
// run and delete; refused crons are readable.
func TestCLISchedules(t *testing.T) {
	p := startCLIPort(t)
	d := p.addDock("api", "support")
	p.ok("agents", "add", "ci")
	p.ok("agents", "assign", "ci", d, "-default")
	p.ok("customs", "default", "allow")
	conv := p.inject(d, "chat-1", "m1", "hi")

	mustContain(t, "no schedules", p.ok("schedules"), "no schedules yet")
	out := p.ok("schedule", "add", conv, "standup", "-cron", "0 9 * * 1-5", "-timezone", "America/Lima", "-text", "daily standup", "-agent", "ci")
	mustContain(t, "add", out, "schedule standup created", "next:  ", "then:  ")
	if strings.Count(out, "then:  ") != 2 {
		t.Errorf("expected three upcoming times:\n%s", out)
	}
	if !regexp.MustCompile(`next:  \w{3} \d{4}-\d\d-\d\d 09:00 `).MatchString(out) {
		t.Errorf("the next time is not 09:00 in the zone:\n%s", out)
	}
	id := firstID(t, out)

	list := p.ok("cron", "list")
	mustContain(t, "list", list, "ID", "CRON", "standup", "0 9 * * 1-5", "America/Lima", "true", id, conv)
	mustContain(t, "list by conversation", p.ok("schedules", "list", conv), "standup")
	var page query.Paginated[struct {
		Name  string `json:"name"`
		Agent string `json:"agent_id"`
	}]
	if code := p.api(http.MethodGet, "/api/v1/schedules", nil, &page); code != http.StatusOK || len(page.Items) != 1 || page.Items[0].Agent == "" {
		t.Fatalf("schedules over the API: %d %+v", code, page)
	}

	prev := p.ok("schedules", "preview", "@hourly", "-timezone", "Europe/Madrid")
	mustContain(t, "preview", prev, "next:  ", "CE")
	if msg := p.fails("schedules", "preview"); !strings.Contains(msg, "usage: seaport schedules preview") {
		t.Errorf("preview usage: %q", msg)
	}

	mustContain(t, "edit", p.ok("schedules", "edit", id, "-cron", "30 8 * * *", "-agent", "none"), "schedule standup saved", "08:30")
	mustContain(t, "disable", p.ok("schedules", "disable", id), "switched off")
	mustContain(t, "disabled in list", p.ok("schedules"), "false")
	mustContain(t, "enable", p.ok("schedules", "enable", id), "schedule standup saved", "next:  ")
	mustContain(t, "run", p.ok("schedules", "run", id), "posted now (ship ")
	mustContain(t, "the run is in history", p.ok("history", conv), "Seaport · standup: daily standup")

	// refusals: too frequent, never firing, a bad zone, no text
	for name, args := range map[string][]string{
		"too frequent": {"schedules", "add", conv, "spam", "-cron", "* * * * *", "-text", "x"},
		"never":        {"schedules", "add", conv, "never", "-cron", "0 0 31 2 *", "-text", "x"},
		"bad zone":     {"schedules", "add", conv, "zone", "-cron", "@daily", "-timezone", "Mars/Olympus", "-text", "x"},
		"no text":      {"schedules", "add", conv, "empty", "-cron", "@daily"},
	} {
		if msg := p.fails(args...); msg == "" {
			t.Errorf("%s: empty message", name)
		} else {
			t.Logf("%s: %s", name, msg)
		}
	}
	if msg := p.fails("schedules", "add", conv, "x", "-cron", "* * * * *", "-text", "x"); !strings.Contains(strings.ToLower(msg), "minute") && !strings.Contains(strings.ToLower(msg), "cron") {
		t.Errorf("a refused cron says nothing about the cron: %q", msg)
	}
	if msg := p.fails("schedules", "add", conv); !strings.Contains(msg, "usage: seaport schedules add") {
		t.Errorf("add usage: %q", msg)
	}
	if msg := p.fails("schedules", "delete", id); !strings.Contains(msg, "add -yes") {
		t.Errorf("delete without -yes: %q", msg)
	}
	mustContain(t, "delete", p.ok("schedules", "delete", id, "-yes"), "schedule deleted")
	mustContain(t, "none left", p.ok("schedules"), "no schedules yet")
	if msg := p.fails("schedules", "frobnicate"); !strings.Contains(msg, "unknown schedules command") {
		t.Errorf("unknown: %q", msg)
	}
}

// Environments: apply from a file (YAML and stdin), list, export, versions,
// a home directory pushed, shown and pulled, delete; builds are refused
// without a runtime.
func TestCLIEnvironments(t *testing.T) {
	p := startCLIPort(t)
	dir := t.TempDir()

	mustContain(t, "no environments", p.ok("env"), "no environments yet")
	file := filepath.Join(dir, "web.yaml")
	if err := os.WriteFile(file, []byte("name: web\ndescription: the web app\nharness: claude-code\npackages: [jq]\nresources: {size: M}\nidle_stop: 2h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "apply", p.ok("env", "apply", "-f", file), "created environment web v1")
	mustContain(t, "apply again", p.ok("envs", "apply", "-f", file), "environment web unchanged (v1)")

	list := p.ok("environments", "list")
	mustContain(t, "list", list, "NAME", "web", "v1", "claude-code", "the web app")

	// a change makes a version; stdin works too
	changed := strings.Replace(mustRead(t, file), "packages: [jq]", "packages: [jq, ripgrep]", 1)
	out, _, err := p.cli(changed, "env", "apply", "-f", "-")
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "apply from stdin", out, "environment web v2")
	exp := p.ok("env", "export", "web")
	mustContain(t, "export", exp, "# web v2, by ", "name: web", "harness: claude-code", "- ripgrep", "idle_stop: 2h")
	old := p.ok("env", "show", "web", "-version", "1")
	mustContain(t, "export v1", old, "# web v1")
	if strings.Contains(old, "ripgrep") {
		t.Error("version 1 has the package of version 2")
	}
	// what export prints applies back unchanged
	back := filepath.Join(dir, "back.yaml")
	if err := os.WriteFile(back, []byte(exp[strings.Index(exp, "\n")+1:]), 0o600); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "export applies back", p.ok("env", "apply", "-f", back), "unchanged (v2)")
	mustContain(t, "versions", p.ok("env", "versions", "web"), "VERSION", "v1", "v2")

	// refusals
	typo := filepath.Join(dir, "typo.yaml")
	if err := os.WriteFile(typo, []byte("name: x\nharnes: claude-code\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := p.fails("env", "apply", "-f", typo); !strings.Contains(msg, "environment file:") {
		t.Errorf("typo in file: %q", msg)
	}
	if msg := p.fails("env", "apply"); !strings.Contains(msg, "usage: seaport env apply") {
		t.Errorf("apply usage: %q", msg)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("name: bad\nharness: emacs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := p.fails("env", "apply", "-f", bad); msg == "" {
		t.Error("an unknown harness was accepted")
	}
	if msg := p.fails("env", "show", "ghost"); msg == "" {
		t.Error("an unknown environment was shown")
	}
	if msg := p.fails("env", "build", "web"); msg == "" {
		t.Error("a build ran without a runtime")
	} else {
		t.Logf("build without a runtime: %s", msg)
	}

	// a home: a directory next to the file is uploaded first
	home := filepath.Join(dir, "claude")
	for path, body := range map[string]string{
		"CLAUDE.md":              "be terse",
		"settings.json":          `{"model":"sonnet"}`,
		"skills/go/SKILL.md":     "---\nname: go\n---\nuse gofmt",
		"projects/x/session.txt": "left out",
	} {
		full := filepath.Join(home, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustContain(t, "no home", p.ok("env", "home", "show", "web"), "has no home")
	dry := p.ok("env", "home", "push", "web", home, "-dry-run")
	mustContain(t, "dry run", dry, "left out (not claude-code config): projects/", "CLAUDE.md", "3 files", "dry run, nothing uploaded")
	pushed := p.ok("env", "home", "push", "web", home)
	mustContain(t, "push", pushed, "uploaded claude-code home from "+home, "3 files", "sha256:", "environment web v3")
	show := p.ok("env", "home", "show", "web")
	mustContain(t, "home show", show, "# web v3: claude-code home sha256:", "3 files", "CLAUDE.md", "settings.json", "skills/go/SKILL.md")

	pull := filepath.Join(dir, "pulled")
	mustContain(t, "pull", p.ok("env", "home", "pull", "web", pull), "3 files of web's home in "+pull)
	if got := mustRead(t, filepath.Join(pull, "skills", "go", "SKILL.md")); got != "---\nname: go\n---\nuse gofmt" {
		t.Errorf("pulled skill = %q", got)
	}
	if _, err := os.Stat(filepath.Join(pull, "projects")); err == nil {
		t.Error("a session file was pushed")
	}
	if msg := p.fails("env", "home", "pull", "web", pull); !strings.Contains(msg, "is not empty") {
		t.Errorf("pull into a non-empty dir: %q", msg)
	}
	if msg := p.fails("env", "home", "pull", "web"); !strings.Contains(msg, "usage: seaport env home pull") {
		t.Errorf("pull usage: %q", msg)
	}
	if msg := p.fails("env", "home", "push", "web", filepath.Join(dir, "nope")); !strings.Contains(msg, "is not a directory") {
		t.Errorf("push of a missing dir: %q", msg)
	}
	if msg := p.fails("env", "home"); !strings.Contains(msg, "usage: seaport env home") {
		t.Errorf("home usage: %q", msg)
	}

	// home: ./dir in an environment file is pushed by apply, relative to the file
	withHome := filepath.Join(dir, "api.yaml")
	if err := os.WriteFile(withHome, []byte("name: api\nharness: claude-code\nhome: ./claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = p.ok("env", "apply", "-f", withHome)
	mustContain(t, "apply with a home dir", out, "uploaded claude-code home", "created environment api v1")
	if got := p.ok("env", "export", "api"); !strings.Contains(got, "home: sha256:") {
		t.Errorf("export without the home digest:\n%s", got)
	}

	// delete needs -yes
	if msg := p.fails("env", "delete", "api"); !strings.Contains(msg, "add -yes") {
		t.Errorf("delete without -yes: %q", msg)
	}
	mustContain(t, "delete", p.ok("env", "delete", "api", "-yes"), "deleted environment api")
	if msg := p.fails("env", "frobnicate"); !strings.Contains(msg, "unknown env command") {
		t.Errorf("unknown: %q", msg)
	}
}

// Errors are readable: a wrong token, an unreachable port, an unknown
// command, a validation failure (422) with the port's own message.
func TestCLIErrors(t *testing.T) {
	p := startCLIPort(t)

	// a wrong token is the port's 401 message; no token explains what to set
	_, _, err := p.cliAs("wrong-token", "", "dock", "list")
	if err == nil {
		t.Fatal("a wrong token listed docks")
	}
	t.Logf("wrong token: %s", errorMessage(err))
	_, _, err = p.cliAs("", "", "dock", "list")
	if err == nil || !strings.Contains(errorMessage(err), "SEAPORT_ADMIN_TOKEN") {
		t.Errorf("no token: %v", err)
	}

	// an unreachable port
	t.Setenv("SEAPORT_URL", "http://127.0.0.1:1")
	t.Setenv("SEAPORT_ADMIN_TOKEN", cliAdminToken)
	var errOut bytes.Buffer
	oldErr := stderr
	stderr = &errOut
	defer func() { stderr = oldErr }()
	err = run(context.Background(), []string{"dock", "list"})
	if err == nil || !strings.Contains(errorMessage(err), "cannot reach the port at http://127.0.0.1:1") {
		t.Errorf("unreachable: %v", err)
	}

	// unknown commands and missing arguments
	if msg := p.fails("frobnicate"); !strings.Contains(msg, "unknown command frobnicate") {
		t.Errorf("unknown command: %q", msg)
	}
	if msg := p.fails("dock"); !strings.Contains(msg, "usage: seaport dock") {
		t.Errorf("dock usage: %q", msg)
	}
	if msg := p.fails("dock", "frobnicate"); !strings.Contains(msg, "unknown dock command frobnicate") {
		t.Errorf("unknown dock command: %q", msg)
	}
	if msg := p.fails("dock", "add"); !strings.Contains(msg, "usage: seaport dock add") {
		t.Errorf("dock add usage: %q", msg)
	}
	if msg := p.fails("dock", "add", "carrier-pigeon"); msg == "" {
		t.Error("an unknown channel was accepted")
	}
	// a 422/400 from the port shows its message, not the status line
	if msg := p.fails("dock", "rename", "not-a-dock-id", "x"); msg == "" || strings.HasPrefix(msg, "4") {
		t.Errorf("rename of a bad id: %q", msg)
	}

	// the help and version
	out, _, err := p.cli("", "help")
	if err != nil || !strings.Contains(out, "seaport - a port where messages arrive") {
		t.Errorf("help: %v %q", err, out)
	}
	out, _, err = p.cli("", "version")
	if err != nil || strings.TrimSpace(out) != bootstrap.Version {
		t.Errorf("version: %v %q", err, out)
	}
	if _, _, err := p.cli(""); err != nil {
		t.Errorf("no arguments prints the usage: %v", err)
	}
	if msg := (errorMessage(context.Canceled)); msg != "context canceled" {
		t.Errorf("untyped error message = %q", msg)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
