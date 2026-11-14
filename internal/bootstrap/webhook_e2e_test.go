package bootstrap_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// hook posts a body to a webhook dock with the given headers and returns
// the status and the JSON answer.
func (p *port) hook(d dock.Dock, query string, headers map[string]string, body string) (int, map[string]any) {
	p.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/hooks/"+d.ID.String()+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

type webhookInfo struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	Template  struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	} `json:"template"`
	HasSecret bool   `json:"has_secret"`
	Secret    string `json:"secret"`
	Preview   *struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	} `json:"preview"`
}

// A webhook dock: linked at once, its secret shown only when rotated, any
// of the proofs wakes the crew with the templated message, a retry is a
// duplicate, the door is read-only (the agent answers on a linked door),
// and unlinking closes the endpoint.
func TestWebhookDock(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.SecretKey = "webhook-test-secret-key-0123456789abcdef" }, bootstrap.Options{})

	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "webhook", "name": "Sentry"}, &d); code != http.StatusCreated {
		t.Fatalf("create webhook dock: %d", code)
	}
	if d.Status != dock.StatusLinked || d.CanReply() {
		t.Fatalf("a new webhook dock is linked and can't reply: %+v", d)
	}
	path := "/api/v1/docks/" + d.ID.String() + "/webhook"
	var info webhookInfo
	if code := p.admin(http.MethodGet, path, nil, &info); code != http.StatusOK || !info.HasSecret || info.Secret != "" ||
		info.URL != p.srv.URL+"/hooks/"+d.ID.String() || info.Reachable {
		t.Fatalf("info: %d %+v", code, info)
	}
	api := p.apiDock("api")
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+api.ID.String()+"/webhook", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("webhook info of an api dock: %d", code)
	}
	// Replies can't be turned on.
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(), map[string]bool{"replies": true}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("replies on a webhook dock: %d", code)
	}

	// The secret is only shown by rotating.
	var rotated webhookInfo
	if code := p.admin(http.MethodPost, path+"/secret", nil, &rotated); code != http.StatusOK || rotated.Secret == "" {
		t.Fatalf("rotate: %d %+v", code, rotated)
	}
	secret := rotated.Secret
	p.admin(http.MethodGet, path, nil, &info)
	if info.Secret != "" || strings.Contains(fmtJSON(info), secret) {
		t.Fatalf("info leaks the secret: %+v", info)
	}

	p.assignAs(adminToken, d.ID, p.agent(testAgent).ID, true)
	p.letIn(d)
	body := `{"event":"issue.created","issue":{"id":"WEB-1","title":"Checkout fails","level":"error"}}`

	// Unknown dock and every wrong proof are the same 404.
	other := dock.Dock{ID: api.ID}
	if code, _ := p.hook(other, "", map[string]string{"Authorization": "Bearer " + secret}, body); code != http.StatusNotFound {
		t.Fatalf("an api dock is no webhook: %d", code)
	}
	for name, h := range map[string]map[string]string{
		"none": nil, "bearer": {"Authorization": "Bearer nope"}, "token": {"X-Seaport-Token": "nope"},
		"signature": {"X-Seaport-Signature": "sha256=00"}, "basic": {"Authorization": secret},
	} {
		if code, _ := p.hook(d, "", h, body); code != http.StatusNotFound {
			t.Fatalf("%s: %d", name, code)
		}
	}
	if code, _ := p.hook(d, "?token=nope", nil, body); code != http.StatusNotFound {
		t.Fatalf("query token: %d", code)
	}

	// A template turns the body into a message; the sample previews it.
	tpl := map[string]string{"chat_id": "{{.issue.id}}", "external_id": "{{.issue.id}}-{{.event}}",
		"text": "[{{.issue.level}}] {{.issue.title}}"}
	var saved webhookInfo
	if code := p.admin(http.MethodPut, path, map[string]any{"template": map[string]string{"text": "{{.a"}}, &saved); code != http.StatusBadRequest {
		t.Fatalf("bad template: %d", code)
	}
	if code := p.admin(http.MethodPut, path, map[string]any{"template": tpl, "sample": json.RawMessage(body)}, &saved); code != http.StatusOK ||
		saved.Preview == nil || saved.Preview.ChatID != "WEB-1" || saved.Preview.Text != "[error] Checkout fails" || saved.Template.ChatID != "{{.issue.id}}" {
		t.Fatalf("save template: %d %+v", code, saved)
	}

	// The bearer wakes the default agent with the templated text.
	conn := p.dialCrew(1)
	code, out := p.hook(d, "", map[string]string{"Authorization": "Bearer " + secret}, body)
	if code != http.StatusAccepted || out["accepted"] != float64(1) || out["ignored"] != float64(0) {
		t.Fatalf("webhook: %d %v", code, out)
	}
	f := conn.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].Text != "[error] Checkout fails" ||
		f.Delivery.Ships[0].ExternalID != "WEB-1-issue.created" {
		t.Fatalf("delivery: %+v", f)
	}
	// The door is read-only, and a reply there is refused.
	if len(f.Delivery.ReadOnly) != 1 || f.Delivery.ReadOnly[0] != f.Delivery.Conversation.ID {
		t.Fatalf("read-only doors: %+v", f.Delivery)
	}
	hookConv := f.Delivery.Conversation.ID
	if f.Delivery.Conversation.Kind != conversation.KindGroup {
		t.Fatalf("conversation kind: %+v", f.Delivery.Conversation)
	}
	tok := p.crewToken()
	session := mcpSession(t, p, tok)
	if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": hookConv.String(), "text": "visto"}}); err != nil || !res.IsError {
		t.Fatalf("reply on a webhook door must be refused: %v %+v", err, res)
	}
	ack(conn, f)

	// The same body again is a duplicate (the provider's retry), whatever the proof.
	for _, h := range []map[string]string{
		{"Authorization": "Bearer " + secret}, {"X-Seaport-Token": secret}, {"X-Seaport-Signature": sign(secret, body)},
	} {
		if code, out := p.hook(d, "", h, body); code != http.StatusAccepted || out["accepted"] != float64(0) || out["ignored"] != float64(1) {
			t.Fatalf("retry with %v: %d %v", h, code, out)
		}
	}
	if code, out := p.hook(d, "?token="+secret, nil, strings.Replace(body, "WEB-1", "WEB-2", 1)); code != http.StatusAccepted || out["accepted"] != float64(1) {
		t.Fatalf("query token: %d %v", code, out)
	}
	f = conn.read()
	ack(conn, f)

	// Not JSON: 415; an array is one message per element (up to 50).
	if code, _ := p.hook(d, "", map[string]string{"X-Seaport-Token": secret}, "hello"); code != http.StatusUnsupportedMediaType {
		t.Fatalf("not JSON: %d", code)
	}
	if code, out := p.hook(d, "", map[string]string{"X-Seaport-Token": secret},
		`[{"event":"a","issue":{"id":"WEB-3","title":"x","level":"info"}},{"event":"a","issue":{"id":"WEB-4","title":"y","level":"info"}}]`); code != http.StatusAccepted ||
		out["accepted"] != float64(2) {
		t.Fatalf("array: %d %v", code, out)
	}
	for range 2 {
		ack(conn, conn.read())
	}

	// The agent answers on a door that takes replies: the thread links the
	// alert's conversation with an api one.
	p.letIn(api)
	a := p.inject(api, "ops", "o1", "hola")
	f = delivered(t, conn, a)
	ack(conn, f)
	var doors []conversation.Conversation
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+hookConv.String()+"/links",
		map[string]string{"conversation_id": a.Conversation.String()}, &doors); code != http.StatusOK {
		t.Fatalf("link: %d", code)
	}
	if code, _ := p.hook(d, "", map[string]string{"X-Seaport-Token": secret}, strings.Replace(body, "WEB-1", "WEB-5", 1)); code != http.StatusAccepted {
		t.Fatalf("webhook on a linked thread: %d", code)
	}
	// WEB-5 is another chat (its own conversation): link it too.
	f = conn.read()
	five := f.Delivery.Conversation.ID
	ack(conn, f)
	if code := p.admin(http.MethodPost, "/api/v1/conversations/"+five.String()+"/links",
		map[string]string{"conversation_id": a.Conversation.String()}, &doors); code != http.StatusOK {
		t.Fatalf("link WEB-5: %d", code)
	}
	if code, _ := p.hook(d, "", map[string]string{"X-Seaport-Token": secret}, `{"event":"again","issue":{"id":"WEB-5","title":"z","level":"error"}}`); code != http.StatusAccepted {
		t.Fatalf("webhook: %d", code)
	}
	f = conn.read()
	if !f.Delivery.CanReply(a.Conversation) || f.Delivery.CanReply(five) {
		t.Fatalf("thread delivery: %+v", f.Delivery)
	}
	if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": a.Conversation.String(), "text": "resuelto"}}); err != nil || res.IsError {
		t.Fatalf("reply on the api door: %v %+v", err, res)
	}
	if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": five.String(), "text": "no"}}); err != nil || !res.IsError {
		t.Fatalf("reply on the webhook door: %v %+v", err, res)
	}
	ack(conn, f)

	// Unlinking closes the endpoint; relinking makes a new secret.
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLoggedOut {
		t.Fatalf("unlink: %d %+v", code, d)
	}
	if code, _ := p.hook(d, "", map[string]string{"X-Seaport-Token": secret}, body); code != http.StatusNotFound {
		t.Fatalf("an unlinked webhook answers: %d", code)
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/relink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLinked {
		t.Fatalf("relink: %d %+v", code, d)
	}
	if code, _ := p.hook(d, "", map[string]string{"X-Seaport-Token": secret}, body); code != http.StatusNotFound {
		t.Fatalf("the old secret must not work: %d", code)
	}
	conn.quiet(100 * time.Millisecond)
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
