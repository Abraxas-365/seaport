package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// A delivery lists the agent's approvals: pending ones with their options,
// answered ones with the choice; the prompt points at the ask tool.
func TestPromptListsApprovals(t *testing.T) {
	c := conversation.Conversation{ID: identity.NewConversationID(), Channel: dock.ChannelSlack, Kind: conversation.KindGroup, Title: "#ops", ExternalID: "C1"}
	yes := "Aprobar"
	answered := ship.Approval{ID: identity.NewApprovalID(), Text: "¿Despliego?", Options: []string{"Aprobar", "Rechazar"},
		Status: ship.ApprovalAnswered, Answer: &yes, AnsweredBy: "Ana"}
	pending := ship.Approval{ID: identity.NewApprovalID(), Text: "¿Borro?", Options: []string{"Sí", "No"}, Status: ship.ApprovalPending}
	d := ship.Delivery{Conversation: c, Thread: []conversation.Conversation{c}, Approvals: []ship.Approval{answered, pending},
		Ships: []ship.Ship{{Conversation: c.ID, SenderName: "Ana", Text: "1", SentAt: time.Now()}}}
	for name, got := range map[string]string{"prompt": Prompt(d, false, nil), "steer": SteerPrompt(d, nil)} {
		for _, want := range []string{answered.ID.String() + ` answered "Aprobar" by Ana`, pending.ID.String() + " pending (options: Sí | No)"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, got)
			}
		}
	}
	if got := Prompt(d, false, nil); !strings.Contains(got, "ask_approval") {
		t.Errorf("prompt names no ask tool:\n%s", got)
	}
	d.Approvals = nil
	if got := Prompt(d, false, nil); strings.Contains(got, "Approvals you asked") {
		t.Errorf("no approvals, no section:\n%s", got)
	}
}

// `seaport ask` posts the question as the agent and prints the outcome.
func TestAskCommand(t *testing.T) {
	conv := identity.NewConversationID()
	var got crew.AskRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != crew.ApprovalsPath || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("request: %s %s %v", r.Method, r.URL.Path, r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		answer := "Sí"
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ship.Approval{ID: identity.NewApprovalID(), Status: ship.ApprovalAnswered, Answer: &answer})
	}))
	defer srv.Close()
	t.Setenv(hosting.EnvURL, srv.URL)
	t.Setenv(hosting.EnvToken, "tok")

	var out bytes.Buffer
	err := AskCommand(context.Background(), []string{conv.String(), "-option", "Sí", "-option", "No", "-wait", "30", "¿Seguimos", "adelante?"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationID != conv || got.Text != "¿Seguimos adelante?" || len(got.Options) != 2 || got.WaitSeconds != 30 {
		t.Fatalf("body: %+v", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "answered Sí") {
		t.Fatalf("output: %q", out.String())
	}
	if err := AskCommand(context.Background(), []string{conv.String(), "-option", "Sí", "¿Seguimos?"}, &out); err == nil {
		t.Fatal("one option must be refused before calling the port")
	}
}
