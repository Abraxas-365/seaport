package bootstrap_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeJira plays Jira Cloud's REST API v3 for one service account
// (accountId 5b10bot, "Seaport Bot") that takes basic auth.
type fakeJira struct {
	t     *testing.T
	srv   *httptest.Server
	email string
	token string

	mu       sync.Mutex
	calls    []string
	comments []map[string]any  // posted bodies
	stamps   map[string]string // comment id → seaport property
	revoked  bool              // the API token stopped working
	nextID   int
}

func newFakeJira(t *testing.T) *fakeJira {
	f := &fakeJira{t: t, email: "bot@example.com", token: "api-token-fake", stamps: map[string]string{}, nextID: 9000}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJira) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	answer := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != f.email || pass != f.token || f.revoked {
		answer(http.StatusUnauthorized, map[string]any{"message": "Client must be authenticated to access this resource."})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/myself":
		answer(http.StatusOK, map[string]any{"accountId": "5b10bot", "displayName": "Seaport Bot", "active": true})
	case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/PROJ-7/comment":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.nextID++
		id := strconv.Itoa(f.nextID)
		f.comments = append(f.comments, body)
		if props, _ := body["properties"].([]any); len(props) == 1 {
			p, _ := props[0].(map[string]any)
			v, _ := p["value"].(map[string]any)
			f.stamps[id], _ = v["message"].(string)
		}
		answer(http.StatusCreated, map[string]any{"id": id})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/api/3/comment/") && strings.HasSuffix(r.URL.Path, "/properties/seaport"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/api/3/comment/"), "/properties/seaport")
		stamp, ok := f.stamps[id]
		if !ok {
			answer(http.StatusNotFound, map[string]any{"errorMessages": []string{"property not found"}})
			return
		}
		answer(http.StatusOK, map[string]any{"key": "seaport", "value": map[string]string{"message": stamp}})
	default:
		answer(http.StatusNotFound, map[string]any{"errorMessages": []string{"not found: " + r.URL.Path}})
	}
}

func (f *fakeJira) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

type jiraInfo struct {
	Site *struct {
		Site        string `json:"site"`
		AccountID   string `json:"account_id"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	} `json:"site"`
	WebhookURL string   `json:"webhook_url"`
	Secret     string   `json:"secret"`
	Hosts      []string `json:"hosts"`
}

// jiraWebhook posts a payload; secret signs it as an admin webhook does
// (X-Hub-Signature), bearer (when set) replaces that with Automation's proof.
func (p *port) jiraWebhook(d dock.Dock, secret, bearer string, payload any) (int, map[string]any) {
	p.t.Helper()
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/jira/webhook/"+d.ID.String(), strings.NewReader(string(raw)))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		req.Header.Set("X-Hub-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func adfText(text string) map[string]any {
	return map[string]any{"type": "doc", "version": 1, "content": []any{
		map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": text}}}}}
}

func adfMention(id, name, rest string) map[string]any {
	return map[string]any{"type": "doc", "version": 1, "content": []any{
		map[string]any{"type": "paragraph", "content": []any{
			map[string]any{"type": "mention", "attrs": map[string]any{"id": id, "text": "@" + name}},
			map[string]any{"type": "text", "text": rest}}}}}
}

func jiraComment(id string, body any, author, accountType string) map[string]any {
	return map[string]any{
		"webhookEvent": "comment_created",
		"comment": map[string]any{"id": id, "body": body, "created": "2024-05-01T10:30:00.000+0000",
			"author": map[string]any{"accountId": author, "displayName": "Person " + author, "emailAddress": author + "@example.com", "accountType": accountType}},
		"issue": map[string]any{"id": "10007", "key": "PROJ-7", "fields": map[string]any{"summary": "Checkout fails"}},
	}
}

// A Jira dock: linked with an API token checked against the site, signed
// (or bearer) webhooks that mention an agent or the account reach the
// team, the reply is a comment in ADF stamped with the message id (so its
// webhook is the stored reply, not a second message), a refused token logs
// the dock out and unlinking forgets the site.
func TestJiraDock(t *testing.T) {
	jira := newFakeJira(t)
	host := strings.TrimPrefix(jira.srv.URL, "http://")
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "jira-test-secret-key-0123456789abcdef"
		c.JiraHosts = []string{host}
	}, bootstrap.Options{})

	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "jira", "name": "Support"}, &d); code != http.StatusCreated {
		t.Fatalf("create jira dock: %d", code)
	}
	if d.Status != dock.StatusLinking {
		t.Fatalf("new jira dock is %s", d.Status)
	}
	path := "/api/v1/docks/" + d.ID.String() + "/jira"
	var info jiraInfo
	if code := p.admin(http.MethodGet, path, nil, &info); code != http.StatusOK || info.Site != nil ||
		info.WebhookURL != p.srv.URL+"/jira/webhook/"+d.ID.String() {
		t.Fatalf("jira info: %d %+v", code, info)
	}
	api := p.apiDock("api")
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+api.ID.String()+"/jira", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("jira info of an api dock: %d", code)
	}

	// Link: sites off the allow-list, bad credentials and a missing body
	// are refused; nothing is stored.
	link := func(body map[string]string, out any) int { return p.admin(http.MethodPut, path, body, out) }
	if code := link(map[string]string{"site": "https://evil.example", "email": jira.email, "api_token": jira.token}, nil); code != http.StatusBadRequest {
		t.Fatalf("site off the allow-list: %d", code)
	}
	if code := link(map[string]string{"site": jira.srv.URL, "email": jira.email, "api_token": "wrong"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong token: %d", code)
	}
	if code := link(map[string]string{"site": jira.srv.URL, "email": "not-an-email", "api_token": jira.token}, nil); code != http.StatusBadRequest {
		t.Fatalf("bad email: %d", code)
	}
	var linked jiraInfo
	if code := link(map[string]string{"site": jira.srv.URL, "email": jira.email, "api_token": jira.token}, &linked); code != http.StatusOK {
		t.Fatalf("link: %d", code)
	}
	if linked.Site == nil || linked.Site.AccountID != "5b10bot" || linked.Site.DisplayName != "Seaport Bot" || linked.Site.Email != jira.email || linked.Secret == "" {
		t.Fatalf("linked = %+v", linked)
	}
	secret := linked.Secret
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLinked || d.Address != host {
		t.Fatalf("linked dock: %s %q", d.Status, d.Address)
	}
	p.admin(http.MethodGet, path, nil, &info)
	if info.Site == nil || info.Secret != "" || strings.Contains(fmtJSON(info), secret) || strings.Contains(fmtJSON(info), jira.token) {
		t.Fatalf("jira info leaks a secret: %+v", info)
	}

	// Webhooks: the secret proves them, as an HMAC or as a bearer.
	p.assignAs(adminToken, d.ID, p.agent(testAgent).ID, true)
	p.letIn(d)
	mention := jiraComment("501", adfText("@test please look at this"), "5b20ana", "atlassian")
	if code, _ := p.jiraWebhook(d, "wrong", "", mention); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if code, _ := p.jiraWebhook(d, "", "wrong", mention); code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: %d", code)
	}
	// Not for the crew: other bots, comments calling no one, other events.
	other := map[string]any{"webhookEvent": "jira:issue_updated", "issue": mention["issue"]}
	for _, c := range []map[string]any{
		jiraComment("502", adfText("@test done"), "5b30auto", "app"),
		jiraComment("503", adfText("lgtm"), "5b20ana", "atlassian"),
		other,
	} {
		if code, out := p.jiraWebhook(d, secret, "", c); code != http.StatusAccepted || out["status"] != "ignored" {
			t.Fatalf("ignored webhook: %d %v", code, out)
		}
	}
	code, out := p.jiraWebhook(d, secret, "", mention)
	if code != http.StatusAccepted || out["status"] != "received" || out["created"] != true {
		t.Fatalf("webhook: %d %v", code, out)
	}
	if code, again := p.jiraWebhook(d, secret, "", mention); code != http.StatusAccepted || again["created"] != false || again["ship_id"] != out["ship_id"] {
		t.Fatalf("redelivery: %d %v", code, again)
	}
	c := p.dialCrew(1)
	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].Text != "@test please look at this" ||
		f.Delivery.Ships[0].ExternalID != "comment-501" || f.Delivery.Ships[0].SenderID != "5b20ana" {
		t.Fatalf("delivery = %+v", f)
	}
	ack(c, f)
	// Mentioning the account itself (a picked mention) works too, here
	// through Automation's bearer; a mention of someone else does not.
	if code, out := p.jiraWebhook(d, "", secret, jiraComment("504", adfMention("5b10bot", "Seaport Bot", " can you check?"), "5b20ana", "atlassian")); code != http.StatusAccepted || out["status"] != "received" {
		t.Fatalf("bearer webhook: %d %v", code, out)
	}
	if code, out := p.jiraWebhook(d, secret, "", jiraComment("505", adfMention("5b99other", "Other", " can you check?"), "5b20ana", "atlassian")); code != http.StatusAccepted || out["status"] != "ignored" {
		t.Fatalf("mention of someone else: %d %v", code, out)
	}
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].Text != "@Seaport Bot can you check?" {
		t.Fatalf("mention delivery = %+v", f)
	}
	ack(c, f)
	// A new issue's summary and description count as a message.
	issue := map[string]any{"webhookEvent": "jira:issue_created", "issue": map[string]any{"id": "10008", "key": "PROJ-8",
		"fields": map[string]any{"summary": "Refund broken", "description": "h2. Steps\n* open the order\n* click {{Refund}}\n\n@test please triage",
			"reporter": map[string]any{"accountId": "5b20ana", "displayName": "Ana"}, "created": "2024-05-01T11:00:00.000+0000"}}}
	if code, out := p.jiraWebhook(d, secret, "", issue); code != http.StatusAccepted || out["status"] != "received" {
		t.Fatalf("issue webhook: %d %v", code, out)
	}
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Conversation.ExternalID != "PROJ-8" || f.Delivery.Ships[0].ExternalID != "issue-10008" ||
		f.Delivery.Ships[0].Text != "Refund broken\n\n## Steps\n- open the order\n- click `Refund`\n\n@test please triage" {
		t.Fatalf("issue delivery = %+v", f.Delivery)
	}
	ack(c, f)

	var convs query.Paginated[conversation.Conversation]
	p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+d.ID.String(), nil, &convs)
	var conv conversation.Conversation
	for _, cv := range convs.Items {
		if cv.ExternalID == "PROJ-7" {
			conv = cv
		}
	}
	if len(convs.Items) != 2 || conv.Title != "PROJ-7: Checkout fails" || conv.Kind != conversation.KindGroup {
		t.Fatalf("conversations = %+v", convs.Items)
	}

	// The reply is a comment: ADF from Markdown, stamped with the message id.
	session := mcpSession(t, p, p.crewToken())
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": conv.ID.String(), "text": "## Result\n\nChecked **fine**:\n\n- one\n  - nested\n\n```go\nfmt.Println(1)\n```\n\nSee [docs](https://example.com/d)."}})
	if err != nil || res.IsError {
		t.Fatalf("reply: %v %+v", err, res)
	}
	jira.mu.Lock()
	comments := append([]map[string]any(nil), jira.comments...)
	jira.mu.Unlock()
	if len(comments) != 1 {
		t.Fatalf("comments = %v", comments)
	}
	body := fmtJSON(comments[0]["body"])
	for _, want := range []string{`"type":"doc"`, `"type":"heading"`, `"type":"strong"`, `"type":"bulletList"`, `"language":"go"`, `"href":"https://example.com/d"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("ADF lacks %s: %s", want, body)
		}
	}
	var stamp string
	jira.mu.Lock()
	for _, s := range jira.stamps {
		stamp = s
	}
	jira.mu.Unlock()
	if !strings.HasPrefix(stamp, "jira-") {
		t.Fatalf("comment stamp = %q", stamp)
	}

	// Jira tells us about the comments of our own account. Seaport's reply
	// (it carries its stamp) is the stored message, no second one; a
	// comment posted some other way is recorded as sent and wakes nobody.
	echo := jiraComment("9001", adfText("Result …"), "5b10bot", "app")
	if code, out := p.jiraWebhook(d, secret, "", echo); code != http.StatusAccepted || out["status"] != "recorded" || out["created"] != false {
		t.Fatalf("echo of our reply: %d %v", code, out)
	}
	code, rec := p.jiraWebhook(d, secret, "", jiraComment("9100", adfText("Posted by the agent with the API"), "5b10bot", "app"))
	if code != http.StatusAccepted || rec["status"] != "recorded" || rec["created"] != true {
		t.Fatalf("agent's own comment: %d %v", code, rec)
	}
	var history query.Paginated[ship.Ship]
	p.admin(http.MethodGet, "/api/v1/conversations/"+conv.ID.String()+"/ships", nil, &history)
	byID := map[string]ship.Ship{}
	for _, h := range history.Items {
		byID[h.ExternalID] = h
	}
	if len(history.Items) != 4 { // 2 inbound, the reply, the agent's own comment
		t.Fatalf("history = %+v", history.Items)
	}
	if own := byID["comment-9100"]; own.Direction != ship.DirectionOut || own.Status != ship.StatusSent || own.SenderID != "5b10bot" {
		t.Fatalf("recorded comment = %+v", own)
	}
	if reply := byID[stamp]; reply.Direction != ship.DirectionOut || !strings.HasPrefix(reply.Text, "## Result") {
		t.Fatalf("reply = %+v", reply)
	}
	c.quiet(300 * time.Millisecond)

	// Files are refused (nothing is posted).
	if res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": conv.ID.String(), "text": "see file",
			"files": []map[string]any{{"name": "a.txt", "data": "aGVsbG8="}}}}); err == nil && !res.IsError {
		t.Fatal("a Jira dock sent a file")
	}
	jira.mu.Lock()
	posted := len(jira.comments)
	jira.revoked = true
	jira.mu.Unlock()
	if posted != 1 {
		t.Fatalf("a refused file reply posted %d comments", posted)
	}

	// A token Jira refuses logs the dock out.
	if res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": conv.ID.String(), "text": "anybody there?"}}); err == nil && !res.IsError {
		t.Fatal("reply with a revoked token succeeded")
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLoggedOut {
		t.Fatalf("dock with a revoked token is %s", d.Status)
	}
	jira.mu.Lock()
	jira.revoked = false
	jira.mu.Unlock()

	// A new secret replaces the old one; linking again keeps the webhook.
	var rotated jiraInfo
	if code := p.admin(http.MethodPost, path+"/secret", nil, &rotated); code != http.StatusOK || rotated.Secret == "" || rotated.Secret == secret {
		t.Fatalf("rotate: %d %+v", code, rotated)
	}
	if code, _ := p.jiraWebhook(d, secret, "", mention); code != http.StatusUnauthorized {
		t.Fatalf("old secret after rotating: %d", code)
	}
	if code, _ := p.jiraWebhook(d, rotated.Secret, "", mention); code != http.StatusAccepted {
		t.Fatalf("new secret: %d", code)
	}
	var again jiraInfo
	if code := link(map[string]string{"site": jira.srv.URL, "email": jira.email, "api_token": jira.token}, &again); code != http.StatusOK || again.Secret != "" {
		t.Fatalf("link again: %d %+v", code, again)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLinked {
		t.Fatalf("relinked dock is %s", d.Status)
	}
	if code, _ := p.jiraWebhook(d, rotated.Secret, "", mention); code != http.StatusAccepted {
		t.Fatalf("secret after linking again: %d", code)
	}

	// Unlinking forgets the site; Jira is not called.
	before := len(jira.calls)
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLoggedOut {
		t.Fatalf("unlink: %d %s", code, d.Status)
	}
	jira.mu.Lock()
	after := len(jira.calls)
	jira.mu.Unlock()
	if after != before {
		t.Fatalf("unlinking called Jira (%d calls)", after-before)
	}
	if code, _ := p.jiraWebhook(d, rotated.Secret, "", mention); code != http.StatusNotFound {
		t.Fatalf("webhook of an unlinked dock: %d", code)
	}
	p.admin(http.MethodGet, path, nil, &info)
	if info.Site != nil {
		t.Fatalf("site after unlinking: %+v", info.Site)
	}
}
