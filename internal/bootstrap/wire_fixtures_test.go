package bootstrap_test

// Wire fixtures: one realistic sample of every live event payload and of the
// main API responses, marshalled with the real JSON tags into
// web/src/lib/__fixtures__/wire.json. The web tests (live.test.ts,
// wire.test.ts) load that file, so a change of shape in Go breaks them until
// the web follows. The committed file must match what this test builds
// (like the generated permissions); regenerate it with
//
//	SEAPORT_UPDATE_FIXTURES=1 go test ./internal/bootstrap -run TestWireFixtures
//
// Only example data here: +3460000000x numbers, example.com, fixed ids and
// times, so the file is stable.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/ship"
)

const wireFixturesFile = "web/src/lib/__fixtures__/wire.json"

// fixtureID makes a fixed UUID: the parse function picks the id's type, n
// makes it distinct.
func fixtureID[T any](parse func(string) (T, error), n int) T {
	id, err := parse(fmt.Sprintf("00000000-0000-4000-8000-%012d", n))
	if err != nil {
		panic(err)
	}
	return id
}

func ptr[T any](v T) *T { return &v }

// wireFixtures builds the document: events by type, API responses by name,
// and the harness home rules.
func wireFixtures() map[string]any {
	at := func(min int) time.Time { return time.Date(2030, 1, 2, 3, min, 0, 0, time.UTC) }

	var (
		dockID   = fixtureID(identity.ParseDockID, 1)
		convID   = fixtureID(identity.ParseConversationID, 2)
		shipID   = fixtureID(identity.ParseShipID, 3)
		outID    = fixtureID(identity.ParseShipID, 4)
		agentID  = fixtureID(identity.ParseAgentID, 5)
		delivery = fixtureID(identity.ParseDeliveryID, 6)
		envID    = fixtureID(identity.ParseEnvironmentID, 7)
		apprID   = fixtureID(identity.ParseApprovalID, 8)
		schedID  = fixtureID(identity.ParseScheduleID, 9)
		attID    = fixtureID(identity.ParseAttachmentID, 10)
		ruleID   = fixtureID(identity.ParseRuleID, 11)
		tokenID  = fixtureID(identity.ParseCrewTokenID, 12)
		doorID   = fixtureID(identity.ParseConversationID, 13)
	)

	dk := dock.Dock{ID: dockID, Channel: dock.ChannelSlack, Name: "Support", Address: "example-workspace",
		Status: dock.StatusLinked, Replies: true, CreatedAt: at(0), UpdatedAt: at(1)}
	linking := dock.Dock{ID: dockID, Channel: dock.ChannelWhatsApp, Name: "Phone", Address: "",
		Status: dock.StatusLinking, Replies: true, CreatedAt: at(0), UpdatedAt: at(1), QRCode: "2@example-pairing-code"}
	trigger := dock.Dock{ID: fixtureID(identity.ParseDockID, 14), Channel: dock.ChannelWebhook, Name: "Alerts",
		Status: dock.StatusLinked, Replies: false, CreatedAt: at(0), UpdatedAt: at(1)}

	conv := conversation.Conversation{ID: convID, Dock: dockID, Channel: dock.ChannelSlack, ExternalID: "C0EXAMPLE/1700000000.000100",
		Kind: conversation.KindGroup, Title: "#support", Access: conversation.AccessAuto, GroupMode: conversation.GroupAll,
		Status: conversation.StatusAllowed, Thread: identity.ThreadOf(convID), Handles: []string{"C0EXAMPLE", "+34600000000"},
		LastActivityAt: at(2), CreatedAt: at(0), UpdatedAt: at(2)}
	direct := conv
	direct.ID, direct.Kind, direct.Title, direct.GroupMode, direct.Access = doorID, conversation.KindDirect, "Ana Example", "", conversation.AccessAllow
	direct.Thread = identity.ThreadOf(doorID)

	attachment := ship.Attachment{ID: attID, Kind: ship.AttachmentImage, MIME: "image/png", Size: 2048,
		Name: "screenshot.png", SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
	inbound := ship.Ship{ID: shipID, Conversation: convID, Direction: ship.DirectionIn, ExternalID: "msg-1",
		SenderID: "+34600000001", SenderName: "Ana Example", Text: "Hello, can you look at this?",
		Status: ship.StatusQueued, Agent: &agentID, SentAt: at(2), CreatedAt: at(2), Attachments: []ship.Attachment{attachment}}
	held := inbound
	held.ID, held.Status, held.Agent, held.Attachments = fixtureID(identity.ParseShipID, 15), ship.StatusHeld, nil, nil
	held.HoldReason = conversation.HoldPending
	outbound := ship.Ship{ID: outID, Conversation: convID, Direction: ship.DirectionOut, ExternalID: "out-1",
		SenderID: "agent", SenderName: "support-bot", Text: "On it.", Status: ship.StatusSent, Agent: &agentID,
		Delivery: &delivery, SentAt: at(3), CreatedAt: at(3)}

	approval := ship.Approval{ID: apprID, Conversation: convID, Agent: agentID, Delivery: &delivery, Ship: outID,
		Text: "Deploy to production?", Options: []string{"Yes", "No"}, Status: ship.ApprovalAnswered,
		Answer: ptr("Yes"), AnsweredBy: "+34600000001 (Ana Example)", AnsweredShip: &shipID,
		ExpiresAt: at(30), CreatedAt: at(3), AnsweredAt: ptr(at(4))}

	turn := ship.Turn{Delivery: delivery, Conversation: convID, Agent: agentID, AgentName: "support-bot",
		Outcome: ship.TurnAcked, StartedAt: at(3), EndedAt: at(4),
		Tools:   []ship.TurnTool{{Title: "Read README.md", Kind: "read", Status: "completed", StartedAt: at(3), EndedAt: ptr(at(3))}},
		Omitted: 2}

	deliveryEvent := ship.DeliveryEvent{State: ship.DeliveryAcked, Delivery: &delivery, Conversation: &convID,
		Ships: []identity.ShipID{shipID}, Count: 1, Owner: "laptop"}

	activity := crew.Activity{Delivery: delivery, Conversation: convID, AgentID: agentID, AgentName: "support-bot", At: at(3),
		Items: []crew.ActivityItem{
			{Kind: crew.ActivityThought, Text: "Let me check the logs."},
			{Kind: crew.ActivityTool, Tool: "call-1", Title: "Read README.md", ToolKind: "read", Status: crew.ToolInProgress},
			{Kind: crew.ActivityMessage, Text: "Found it."},
		}}

	status := crew.Status{Agents: []crew.Agent{{Session: "session-1", AgentID: agentID, AgentName: "support-bot",
		Name: "laptop", Concurrency: 2, Steer: true, ProfileVersion: 3, Outdated: false, Remote: "203.0.113.7",
		ConnectedAt: at(0), Deliveries: []crew.AgentDelivery{{ID: delivery, Conversation: convID, Ships: 1, Since: at(3)}}}}}

	libAgent := agent.Agent{ID: agentID, Name: "support-bot", Description: "Answers support questions", CreatedBy: "admin",
		CreatedAt: at(0), UpdatedAt: at(1), ProfileVersion: 3,
		Docks:         []agent.Assignment{{Dock: dockID, Default: true}},
		LoginRequests: agent.LoginRequests{{Host: "app.example.com", Reason: "read the dashboard", At: at(2)}}}

	sched := schedule.Schedule{ID: schedID, Conversation: convID, Agent: &agentID, Name: "Daily summary", Cron: "0 9 * * 1-5",
		Timezone: "UTC", Text: "Summarize yesterday.", Enabled: true, NextAt: at(10), LastAt: ptr(at(1)), LastShip: &shipID,
		CreatedAt: at(0), UpdatedAt: at(1), Preview: []time.Time{at(10), at(20)}}

	env := hosting.Environment{ID: envID, Name: "default", Version: 2, CreatedBy: "admin", CreatedAt: at(0), UpdatedAt: at(1), Agents: 1,
		Spec: hosting.Spec{Description: "Go toolchain", Harness: "claude-code", Image: "", Packages: []string{"git", "make"},
			Setup: "echo ready", RepoConfig: "ignore", Home: "",
			Repos:  []hosting.Repo{{URL: "https://example.com/org/repo.git", Ref: "main", Path: "repo", Credential: "${GIT_TOKEN}", OnStart: "make deps"}},
			Models: hosting.Models{Anthropic: "${ANTHROPIC_KEY}"}, Network: hosting.Network{Egress: []string{"example.com", "*.example.org"}},
			Browser: hosting.Browser{Engine: "chromium"}, Resources: hosting.Resources{Size: "M", CPUs: 2, MemoryMB: 4096},
			Workspace: "persistent", IdleStop: "30m"}}

	build := hosting.Build{Environment: envID, Version: 2, State: "ready", Base: "ghcr.io/example/seaport-claude-code:latest",
		Image: "sha256:0123456789abcdef", Detail: "", StartedAt: at(0), FinishedAt: ptr(at(1)), UpdatedAt: at(1)}

	hosted := hosting.Hosted{Agent: agentID, Environment: envID, UpdatedBy: "admin", UpdatedAt: at(1),
		Slots: []hosting.Slot{{Index: 1, State: "running", Detail: "", Image: "sha256:0123456789abcdef",
			EnvironmentVersion: 2, StartedAt: ptr(at(0)), ActiveAt: ptr(at(2)), UpdatedAt: at(2)}}}

	customs := conversation.Customs{
		Policy: conversation.Policy{Dock: dockID, Default: conversation.DefaultAsk, GroupMode: conversation.GroupMentions},
		Rules:  []conversation.Rule{{ID: ruleID, Dock: dockID, Effect: conversation.EffectAllow, Pattern: "+346000000*", CreatedAt: at(0)}}}

	token := crew.Token{ID: tokenID, Name: "laptop", Agent: agentID, AgentName: "support-bot", Hint: "sp_crew_AbCd",
		CreatedBy: "admin", CreatedAt: at(0), ExpiresAt: nil, LastUsedAt: ptr(at(2))}

	page := func(total int) query.Page { return query.Page{Total: total, Limit: 20, Offset: 0} }

	events := map[string]any{
		dock.EventUpdated:               dk,
		dock.EventDeleted:               dock.Deleted{ID: dockID},
		conversation.EventUpdated:       conv,
		conversation.EventCustoms:       customs,
		ship.EventCreated:               inbound,
		ship.EventUpdated:               outbound,
		ship.EventDelivery:              deliveryEvent,
		ship.EventTurn:                  turn,
		ship.EventApproval:              approval,
		crew.EventChanged:               status,
		crew.EventActivity:              activity,
		agent.EventUpdated:              libAgent,
		agent.EventDeleted:              agent.Deleted{ID: agentID},
		hosting.EventEnvironmentUpdated: env,
		hosting.EventEnvironmentDeleted: hosting.EnvironmentDeleted{ID: envID},
		hosting.EventSlotsUpdated:       hosted,
		hosting.EventEnvironmentBuild:   build,
		hosting.EventSlotBrowser:        hosting.BrowserEvent{Agent: agentID, Slot: 1, State: hosting.BrowserOpened, By: "admin"},
		schedule.EventCreated:           sched,
		schedule.EventUpdated:           sched,
		schedule.EventDeleted:           sched,
		schedule.EventFired:             sched,
	}

	api := map[string]any{
		"dock":            dk,
		"dock_linking":    linking,
		"dock_trigger":    trigger,
		"docks":           query.Paginated[dock.Dock]{Items: []dock.Dock{dk, trigger}, Page: page(2)},
		"conversation":    conv,
		"conversation_dm": direct,
		"inbox_item":      ship.InboxItem{Conversation: conv, Summary: ship.Summary{Last: &outbound, Held: 1, Queued: 0, InFlight: true}},
		"ship_in":         inbound,
		"ship_held":       held,
		"ship_out":        outbound,
		"ships":           query.Paginated[ship.Ship]{Items: []ship.Ship{outbound, inbound}, Page: page(2)},
		"delivery":        ship.Delivery{ID: delivery, Conversation: conv, Thread: []conversation.Conversation{conv, direct}, ReadOnly: []identity.ConversationID{doorID}, Approvals: []ship.Approval{approval}, Ships: []ship.Ship{inbound}},
		"approval":        approval,
		"approvals":       query.Paginated[ship.Approval]{Items: []ship.Approval{approval}, Page: page(1)},
		"turns":           query.Paginated[ship.Turn]{Items: []ship.Turn{turn}, Page: page(1)},
		"customs":         customs,
		"agent":           libAgent,
		"agents":          query.Paginated[agent.Agent]{Items: []agent.Agent{libAgent}, Page: page(1)},
		"crew_status":     status,
		"crew_token":      token,
		"schedule":        sched,
		"schedules":       query.Paginated[schedule.Schedule]{Items: []schedule.Schedule{sched}, Page: page(1)},
		"environment":     env,
		"environments":    query.Paginated[hosting.Environment]{Items: []hosting.Environment{env}, Page: page(1)},
		"build":           build,
		"hosted":          hosted,
	}

	return map[string]any{
		"events":     events,
		"api":        api,
		"home_rules": hosting.HomeRulesOf(),
	}
}

// encodeWireFixtures renders the document with sorted keys (a map round trip
// sorts them) and a trailing newline.
func encodeWireFixtures(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(wireFixtures())
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(generic); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestWireFixturesAreFresh fails when a payload's JSON changed without
// regenerating web/src/lib/__fixtures__/wire.json.
func TestWireFixturesAreFresh(t *testing.T) {
	path := filepath.Join("..", "..", filepath.FromSlash(wireFixturesFile))
	want := encodeWireFixtures(t)
	if os.Getenv("SEAPORT_UPDATE_FIXTURES") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: run `SEAPORT_UPDATE_FIXTURES=1 go test ./internal/bootstrap -run TestWireFixtures`", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale: run `SEAPORT_UPDATE_FIXTURES=1 go test ./internal/bootstrap -run TestWireFixtures`", wireFixturesFile)
	}
}

// TestWireFixturesCoverEveryEvent keeps the sample set complete: one payload
// per event type of EventPermissions, and no payload of a type that doesn't
// exist.
func TestWireFixturesCoverEveryEvent(t *testing.T) {
	events := wireFixtures()["events"].(map[string]any)
	var missing, extra []string
	for typ := range bootstrap.EventPermissions {
		if _, ok := events[typ]; !ok {
			missing = append(missing, typ)
		}
	}
	for typ := range events {
		if _, ok := bootstrap.EventPermissions[typ]; !ok {
			extra = append(extra, typ)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("event types without a wire fixture: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("wire fixtures of unknown event types: %v", extra)
	}
}
