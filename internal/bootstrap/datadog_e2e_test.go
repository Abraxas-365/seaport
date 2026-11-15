package bootstrap_test

import (
	"context"
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
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// datadog posts a body to a Datadog dock and returns the status and answer.
func (p *port) datadog(d dock.Dock, headers map[string]string, body string) (int, map[string]any) {
	p.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/datadog/webhook/"+d.ID.String(), strings.NewReader(body))
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

type datadogInfo struct {
	URL       string `json:"url"`
	Payload   string `json:"payload"`
	HasSecret bool   `json:"has_secret"`
	Secret    string `json:"secret"`
	Mirror    *struct {
		Dock        identity.DockID `json:"dock_id"`
		Destination string          `json:"destination"`
	} `json:"mirror"`
}

func alert(id, monitor, transition, scope string) string {
	b, _ := json.Marshal(map[string]string{
		"id": id, "alert_id": monitor, "transition": transition, "title": "[" + transition + "] CPU high on web",
		"status": "CPU is above 90%", "scope": scope, "tags": "env:prod,service:web,team:platform",
		"message": "@webhook-seaport check the pods", "link": "https://app.datadoghq.com/event/event?id=" + id,
		"priority": "P2", "date": "1700000000000", "org": "acme",
	})
	return string(b)
}

// A Datadog dock: linked at once with a secret shown only when rotated,
// alerts of one monitor and scope land in one conversation (recoveries
// too), the door is read-only, and with a mirror the first alert of a
// thread opens a conversation at the destination, linked to the alert's,
// where the agent answers.
func TestDatadogDock(t *testing.T) {
	p := startPortWith(t, func(c *config.Server) { c.SecretKey = "datadog-test-secret-key-0123456789abcdef" }, bootstrap.Options{})

	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "datadog", "name": "Monitors"}, &d); code != http.StatusCreated {
		t.Fatalf("create datadog dock: %d", code)
	}
	if d.Status != dock.StatusLinked || d.CanReply() {
		t.Fatalf("a new datadog dock is linked and can't reply: %+v", d)
	}
	path := "/api/v1/docks/" + d.ID.String() + "/datadog"
	var info datadogInfo
	if code := p.admin(http.MethodGet, path, nil, &info); code != http.StatusOK || !info.HasSecret || info.Secret != "" ||
		info.URL != p.srv.URL+"/datadog/webhook/"+d.ID.String() || !strings.Contains(info.Payload, "$ALERT_ID") || info.Mirror != nil {
		t.Fatalf("info: %d %+v", code, info)
	}
	chat := p.apiDock("chat")
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+chat.ID.String()+"/datadog", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("datadog info of an api dock: %d", code)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+d.ID.String(), map[string]bool{"replies": true}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("replies on a datadog dock: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+d.ID.String()+"/destinations/x", map[string]string{"target": "x"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("destinations on a datadog dock: %d", code)
	}

	var rotated datadogInfo
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
	first := alert("1001", "4242", "Triggered", "host:web-1")

	// Unknown dock and every wrong proof are the same 404.
	if code, _ := p.datadog(dock.Dock{ID: chat.ID}, map[string]string{"X-Seaport-Token": secret}, first); code != http.StatusNotFound {
		t.Fatalf("an api dock is no datadog webhook: %d", code)
	}
	for name, h := range map[string]map[string]string{
		"none": nil, "bearer": {"Authorization": "Bearer nope"}, "token": {"X-Seaport-Token": "nope"}, "basic": {"Authorization": secret},
	} {
		if code, _ := p.datadog(d, h, first); code != http.StatusNotFound {
			t.Fatalf("%s: %d", name, code)
		}
	}
	// Not JSON, or no monitor: refused.
	if code, _ := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, "hello"); code != http.StatusUnsupportedMediaType {
		t.Fatalf("not JSON: %d", code)
	}
	if code, _ := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, `{"title":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("no monitor: %d", code)
	}

	// The alert wakes the agent; the door is read-only.
	conn := p.dialCrew(1)
	code, out := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, first)
	if code != http.StatusAccepted || out["accepted"] != true || out["mirrored"] != false {
		t.Fatalf("alert: %d %v", code, out)
	}
	f := conn.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ExternalID != "event-1001" ||
		!strings.Contains(f.Delivery.Ships[0].Text, "**Triggered**") || !strings.Contains(f.Delivery.Ships[0].Text, "env:prod") {
		t.Fatalf("delivery: %+v", f)
	}
	alertConv := f.Delivery.Conversation
	if alertConv.ExternalID != "monitor/4242/host:web-1" || alertConv.Kind != conversation.KindGroup || !strings.Contains(alertConv.Title, "host:web-1") {
		t.Fatalf("conversation: %+v", alertConv)
	}
	if len(f.Delivery.ReadOnly) != 1 || f.Delivery.ReadOnly[0] != alertConv.ID {
		t.Fatalf("read-only doors: %+v", f.Delivery)
	}
	ack(conn, f)
	// Datadog's retry is a duplicate.
	if code, out := p.datadog(d, map[string]string{"Authorization": "Bearer " + secret}, first); code != http.StatusAccepted || out["accepted"] != false {
		t.Fatalf("retry: %d %v", code, out)
	}
	// The recovery lands in the same conversation.
	if code, out := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, alert("1002", "4242", "Recovered", "host:web-1")); code != http.StatusAccepted || out["accepted"] != true {
		t.Fatalf("recovery: %d %v", code, out)
	}
	f = conn.read()
	if f.Delivery.Conversation.ID != alertConv.ID || !strings.Contains(f.Delivery.Ships[0].Text, "**Recovered**") {
		t.Fatalf("recovery delivery: %+v", f.Delivery)
	}
	ack(conn, f)

	// A mirror: a destination of a dock that replies; a Datadog dock or a
	// missing destination are refused.
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+chat.ID.String()+"/destinations/oncall",
		map[string]string{"target": "oncall-feed", "title": "on-call"}, nil); code != http.StatusOK {
		t.Fatalf("put destination: %d", code)
	}
	if code := p.admin(http.MethodPut, path, map[string]any{"mirror": map[string]string{"dock_id": d.ID.String(), "destination": "oncall"}}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("a datadog dock as mirror: %d", code)
	}
	if code := p.admin(http.MethodPut, path, map[string]any{"mirror": map[string]string{"dock_id": chat.ID.String(), "destination": "nope"}}, nil); code != http.StatusNotFound {
		t.Fatalf("a missing destination as mirror: %d", code)
	}
	var saved datadogInfo
	if code := p.admin(http.MethodPut, path, map[string]any{"mirror": map[string]string{"dock_id": chat.ID.String(), "destination": "oncall"}}, &saved); code != http.StatusOK ||
		saved.Mirror == nil || saved.Mirror.Dock != chat.ID || saved.Mirror.Destination != "oncall" {
		t.Fatalf("save mirror: %d %+v", code, saved)
	}

	// An alert whose conversation has no other door is mirrored, even an
	// old one (the mirror came after its first alert); then the thread
	// has two doors and later alerts open nothing.
	if code, out := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, alert("1003", "4242", "Re-notify", "host:web-1")); code != http.StatusAccepted || out["mirrored"] != true {
		t.Fatalf("re-notify: %d %v", code, out)
	}
	f = conn.read()
	if len(f.Delivery.Thread) != 2 || f.Delivery.Conversation.ID != alertConv.ID {
		t.Fatalf("re-notify delivery: %+v", f.Delivery)
	}
	ack(conn, f)
	if code, out := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, alert("1004", "4242", "Re-notify", "host:web-1")); code != http.StatusAccepted || out["mirrored"] != false {
		t.Fatalf("re-notify again: %d %v", code, out)
	}
	ack(conn, conn.read())
	// A new monitor is mirrored too. An api destination is one chat (like
	// a WhatsApp group; Slack makes a thread per opening), so both monitors
	// share the mirrored door and the thread has three doors; the agent
	// answers on the mirrored one.
	code, out = p.datadog(d, map[string]string{"X-Seaport-Token": secret}, alert("2001", "7", "Triggered", ""))
	if code != http.StatusAccepted || out["accepted"] != true || out["mirrored"] != true {
		t.Fatalf("mirrored alert: %d %v", code, out)
	}
	f = conn.read()
	if len(f.Delivery.Thread) != 3 || f.Delivery.Conversation.ExternalID != "monitor/7/*" {
		t.Fatalf("mirrored delivery: %+v", f.Delivery)
	}
	var mirrored identity.ConversationID
	for _, door := range f.Delivery.Thread {
		if door.Dock == chat.ID {
			mirrored = door.ID
			if door.ExternalID != "oncall-feed" || door.Status != conversation.StatusAllowed {
				t.Fatalf("mirrored door: %+v", door)
			}
		}
	}
	if mirrored.IsZero() || f.Delivery.CanReply(f.Delivery.Conversation.ID) || !f.Delivery.CanReply(mirrored) {
		t.Fatalf("mirrored thread: %+v", f.Delivery)
	}
	// The summaries were posted there (outbound ships, sent).
	var hist struct {
		Items []struct {
			Direction string `json:"direction"`
			Status    string `json:"status"`
			Text      string `json:"text"`
		} `json:"items"`
	}
	if code := p.admin(http.MethodGet, "/api/v1/conversations/"+mirrored.String()+"/ships", nil, &hist); code != http.StatusOK || len(hist.Items) != 2 ||
		hist.Items[0].Direction != "out" || hist.Items[0].Status != "sent" || !strings.Contains(hist.Items[0].Text, "**Triggered**") ||
		!strings.Contains(hist.Items[1].Text, "**Re-notify**") {
		t.Fatalf("mirrored summaries: %d %+v", code, hist)
	}
	session := mcpSession(t, p, p.crewToken())
	if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": mirrored.String(), "text": "mirando"}}); err != nil || res.IsError {
		t.Fatalf("reply on the mirrored door: %v %+v", err, res)
	}
	if res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": f.Delivery.Conversation.ID.String(), "text": "no"}}); err != nil || !res.IsError {
		t.Fatalf("reply on the datadog door: %v %+v", err, res)
	}
	ack(conn, f)
	// Its recovery lands in the thread, once.
	if code, out := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, alert("2002", "7", "Recovered", "")); code != http.StatusAccepted || out["mirrored"] != false {
		t.Fatalf("recovery of a mirrored alert: %d %v", code, out)
	}
	f = conn.read()
	if len(f.Delivery.Thread) != 3 || f.Delivery.Conversation.ExternalID != "monitor/7/*" {
		t.Fatalf("recovery delivery: %+v", f.Delivery)
	}
	ack(conn, f)

	// Clearing the mirror; removing the destination clears it too.
	if code := p.admin(http.MethodPut, path, map[string]any{"mirror": nil}, &saved); code != http.StatusOK || saved.Mirror != nil {
		t.Fatalf("clear mirror: %d %+v", code, saved)
	}
	p.admin(http.MethodPut, path, map[string]any{"mirror": map[string]string{"dock_id": chat.ID.String(), "destination": "oncall"}}, nil)
	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+chat.ID.String()+"/destinations/oncall", nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove destination: %d", code)
	}
	if code := p.admin(http.MethodGet, path, nil, &info); code != http.StatusOK || info.Mirror != nil {
		t.Fatalf("mirror after its destination went: %d %+v", code, info)
	}

	// Unlinking closes the endpoint; relinking makes a new secret.
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLoggedOut {
		t.Fatalf("unlink: %d %+v", code, d)
	}
	if code, _ := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, first); code != http.StatusNotFound {
		t.Fatalf("an unlinked datadog webhook answers: %d", code)
	}
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/relink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLinked {
		t.Fatalf("relink: %d %+v", code, d)
	}
	if code, _ := p.datadog(d, map[string]string{"X-Seaport-Token": secret}, first); code != http.StatusNotFound {
		t.Fatalf("the old secret still works: %d", code)
	}
	conn.quiet(100 * time.Millisecond)
}
