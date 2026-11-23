package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func mcpReply(conv identity.ConversationID, text string) *mcp.CallToolParams {
	return &mcp.CallToolParams{Name: "reply", Arguments: map[string]any{"conversation_id": conv.String(), "text": text}}
}

// The ship domain can't import schedule, so its list of Seaport's own
// senders is checked here.
func TestSystemScheduleSenderMatches(t *testing.T) {
	if ship.SystemSchedule != schedule.SenderID {
		t.Fatalf("ship.SystemSchedule %q != schedule.SenderID %q", ship.SystemSchedule, schedule.SenderID)
	}
}

// cloudwatchTemplate is the template of docs/guides/cloudwatch.md.
func cloudwatchTemplate() map[string]string {
	return map[string]string{
		"chat_id":     "{{.detail.alarmName}}",
		"chat_title":  "CloudWatch · {{.detail.alarmName}}",
		"external_id": "{{.id}}",
		"sender_id":   "cloudwatch",
		"sender_name": "CloudWatch",
		"text": "**{{.detail.state.value}}** {{.detail.alarmName}} ({{.region}}, account {{.account}})\n" +
			"{{.detail.state.reason}}\nPrevious: {{.detail.previousState.value}}",
	}
}

// cloudwatchEvent is EventBridge's "CloudWatch Alarm State Change" event
// (the shape of AWS's documented sample for a single metric alarm).
func cloudwatchEvent(id, alarm, state, previous string) string {
	b, _ := json.Marshal(map[string]any{
		"version": "0", "id": id, "detail-type": "CloudWatch Alarm State Change", "source": "aws.cloudwatch",
		"account": "123456789012", "time": "2019-10-02T17:04:40Z", "region": "us-east-1",
		"resources": []string{"arn:aws:cloudwatch:us-east-1:123456789012:alarm:" + alarm},
		"detail": map[string]any{
			"alarmName":     alarm,
			"state":         map[string]any{"value": state, "reason": "Threshold Crossed: 1 out of the last 1 datapoints was greater than the threshold (50.0)."},
			"previousState": map[string]any{"value": previous, "reason": "Unchecked"},
		},
	})
	return string(b)
}

// mirrorRig is a port with a Slack dock (destination "oncall") and a
// webhook dock that mirrors to it, both worked by the test agent.
type mirrorRig struct {
	p      *port
	sl     *fakeSlack
	slack  dock.Dock
	hook   dock.Dock
	secret string
}

func newMirrorRig(t *testing.T) *mirrorRig {
	t.Helper()
	sl := newFakeSlack(t)
	sl.uniqueTS = true
	p := newSlackPort(t, sl)
	r := &mirrorRig{p: p, sl: sl}

	r.slack = p.slackDock("On-call")
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+r.slack.ID.String()+"/slack/connect",
		map[string]string{"signing_secret": slSigning, "bot_token": slBotToken}, nil); code != http.StatusOK {
		t.Fatalf("connect slack: %d", code)
	}
	p.assignAs(adminToken, r.slack.ID, p.agent(testAgent).ID, true)
	p.letIn(r.slack)
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+r.slack.ID.String()+"/destinations/oncall",
		map[string]string{"target": "C0ONCALL", "title": "on-call"}, nil); code != http.StatusOK {
		t.Fatalf("put destination: %d", code)
	}
	r.hook, r.secret = r.webhook("Alerts", true)
	return r
}

// webhook makes a webhook dock with the CloudWatch template, mirrored to
// the Slack destination, and lets its chats in when open.
func (r *mirrorRig) webhook(name string, open bool) (dock.Dock, string) {
	r.p.t.Helper()
	var d dock.Dock
	if code := r.p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "webhook", "name": name}, &d); code != http.StatusCreated {
		r.p.t.Fatalf("create webhook dock: %d", code)
	}
	var rotated webhookInfo
	path := "/api/v1/docks/" + d.ID.String() + "/webhook"
	if code := r.p.admin(http.MethodPost, path+"/secret", nil, &rotated); code != http.StatusOK {
		r.p.t.Fatalf("rotate: %d", code)
	}
	if code := r.p.admin(http.MethodPut, path, map[string]any{"template": cloudwatchTemplate()}, nil); code != http.StatusOK {
		r.p.t.Fatalf("template: %d", code)
	}
	r.p.assignAs(adminToken, d.ID, r.p.agent(testAgent).ID, true)
	if open {
		r.p.letIn(d)
	}
	r.setMirror(d)
	return d, rotated.Secret
}

func (r *mirrorRig) setMirror(d dock.Dock) {
	r.p.t.Helper()
	if code := r.p.admin(http.MethodPut, "/api/v1/docks/"+d.ID.String()+"/mirror",
		map[string]any{"mirror": map[string]string{"dock_id": r.slack.ID.String(), "destination": "oncall"}}, nil); code != http.StatusOK {
		r.p.t.Fatalf("set mirror: %d", code)
	}
}

func (r *mirrorRig) send(d dock.Dock, secret, body string) {
	r.p.t.Helper()
	if code, out := r.p.hook(d, "", map[string]string{"Authorization": "Bearer " + secret}, body); code != http.StatusAccepted || out["accepted"] != float64(1) {
		r.p.t.Fatalf("webhook: %d %v", code, out)
	}
}

// slackPosts counts the messages the fake Slack took at the destination.
func (r *mirrorRig) slackPosts() int {
	n := 0
	for _, post := range r.sl.posted() {
		if post["channel"] == "C0ONCALL" {
			n++
		}
	}
	return n
}

// A CloudWatch alarm on a webhook dock with a mirror: the first event of
// an alarm opens a Slack thread before the message is stored, the thread
// is one with the webhook door (read-only), the agent works both, people
// answer in Slack, the OK event lands in the same thread without a second
// post, a second alarm is a second Slack thread.
func TestMirrorOpensAThreadForAnAlarm(t *testing.T) {
	r := newMirrorRig(t)
	p := r.p
	c := p.dialCrew(1)

	r.send(r.hook, r.secret, cloudwatchEvent("evt-1", "ServerCpuTooHigh", "ALARM", "OK"))
	if n := r.slackPosts(); n != 1 {
		t.Fatalf("slack posts after the alarm: %d", n)
	}
	post := r.sl.posted()[0]
	if text, _ := post["text"].(string); !strings.Contains(text, "**ALARM** ServerCpuTooHigh (us-east-1, account 123456789012)") ||
		!strings.Contains(text, "Previous: OK") {
		t.Fatalf("mirror post: %v", post)
	}
	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].ExternalID != "evt-1" || len(f.Delivery.Thread) != 2 {
		t.Fatalf("delivery: %+v", f.Delivery)
	}
	alarmConv := f.Delivery.Conversation
	var slackDoor identity.ConversationID
	for _, door := range f.Delivery.Thread {
		if door.Dock == r.slack.ID {
			slackDoor = door.ID
			if door.ExternalID != "C0ONCALL/1700000100.100001" {
				t.Fatalf("slack door: %+v", door)
			}
		}
	}
	if alarmConv.Dock != r.hook.ID || slackDoor.IsZero() || f.Delivery.CanReply(alarmConv.ID) || !f.Delivery.CanReply(slackDoor) {
		t.Fatalf("doors: %+v", f.Delivery)
	}
	if len(f.Delivery.ReadOnly) != 1 || f.Delivery.ReadOnly[0] != alarmConv.ID {
		t.Fatalf("read-only doors: %+v", f.Delivery.ReadOnly)
	}
	ack(c, f)

	// The agent answers on the Slack door (in the thread under the post).
	session := mcpSession(t, p, p.crewToken())
	if res, err := session.CallTool(t.Context(), mcpReply(slackDoor, "looking at it")); err != nil || res.IsError {
		t.Fatalf("reply on the slack door: %v %+v", err, res)
	}
	if last := r.sl.posted()[len(r.sl.posted())-1]; last["thread_ts"] != "1700000100.100001" || last["text"] != "looking at it" {
		t.Fatalf("reply post: %v", last)
	}

	// A person answers in the Slack thread: same agent, same thread.
	if code, out := p.slackEvent(r.slack, slSigning, time.Now(), false,
		slMessage("message", "C0ONCALL", "1700000200.000100", "1700000100.100001", "I'm on it", nil)); code != http.StatusOK || out["status"] != "accepted" {
		t.Fatalf("answer in the thread: %d %v", code, out)
	}
	f = c.read()
	if f.Delivery.Ships[0].Conversation != slackDoor || len(f.Delivery.Thread) != 2 {
		t.Fatalf("answer delivery: %+v", f.Delivery)
	}
	ack(c, f)

	// The OK event of the same alarm: same webhook door, no second post.
	r.send(r.hook, r.secret, cloudwatchEvent("evt-2", "ServerCpuTooHigh", "OK", "ALARM"))
	f = c.read()
	if f.Delivery.Ships[0].Conversation != alarmConv.ID || !strings.Contains(f.Delivery.Ships[0].Text, "**OK**") || len(f.Delivery.Thread) != 2 {
		t.Fatalf("OK delivery: %+v", f.Delivery)
	}
	ack(c, f)
	if n := r.slackPosts(); n != 2 { // the opening and the agent's reply
		t.Fatalf("slack posts after the OK: %d", n)
	}

	// Another alarm is another Slack thread.
	r.send(r.hook, r.secret, cloudwatchEvent("evt-3", "DiskFull", "ALARM", "OK"))
	f = c.read()
	if f.Delivery.Conversation.ExternalID != "DiskFull" || len(f.Delivery.Thread) != 2 || f.Delivery.Conversation.ID == alarmConv.ID {
		t.Fatalf("second alarm: %+v", f.Delivery)
	}
	if n := r.slackPosts(); n != 3 {
		t.Fatalf("slack posts after the second alarm: %d", n)
	}
	ack(c, f)
}

// Customs holds a conversation: nothing is opened, and allowing it later
// does not open late (the mirror is for the arrival that wakes the crew).
func TestMirrorSkipsHeldConversations(t *testing.T) {
	r := newMirrorRig(t)
	p := r.p
	pending, secret := r.webhook("Pending", false)

	r.send(pending, secret, cloudwatchEvent("evt-p1", "QuietAlarm", "ALARM", "OK"))
	if n := r.slackPosts(); n != 0 {
		t.Fatalf("a held conversation opened a mirror: %d posts", n)
	}
	var convs query.Paginated[conversation.Conversation]
	p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+pending.ID.String(), nil, &convs)
	if len(convs.Items) != 1 || convs.Items[0].Allowed() {
		t.Fatalf("conversations: %+v", convs.Items)
	}
	if code := p.admin(http.MethodPatch, "/api/v1/conversations/"+convs.Items[0].ID.String(), map[string]string{"access": "allow"}, nil); code != http.StatusOK {
		t.Fatalf("allow: %d", code)
	}
	c := p.dialCrew(1)
	f := c.read() // the held message is released to the agent…
	ack(c, f)
	if n := r.slackPosts(); n != 0 || len(f.Delivery.Thread) > 1 {
		t.Fatalf("allowing opened a mirror late: %d posts, thread %d", n, len(f.Delivery.Thread))
	}
}

// Parallel first events of one new alarm open exactly one mirror.
func TestMirrorOpensOnceUnderConcurrency(t *testing.T) {
	r := newMirrorRig(t)
	r.sl.postDelay = 300 * time.Millisecond // without the lock, every event would see a thread of one
	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := cloudwatchEvent("evt-burst-"+string(rune('a'+i)), "BurstAlarm", "ALARM", "OK")
			if code, _ := r.p.hook(r.hook, "", map[string]string{"Authorization": "Bearer " + r.secret}, body); code != http.StatusAccepted {
				t.Errorf("burst %d: %d", i, code)
			}
		}()
	}
	wg.Wait()
	if got := r.slackPosts(); got != 1 {
		t.Fatalf("%d mirror openings for one alarm, want 1", got)
	}
	var convs query.Paginated[conversation.Conversation]
	if code := r.p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+r.hook.ID.String(), nil, &convs); code != http.StatusOK || len(convs.Items) != 1 {
		t.Fatalf("webhook conversations: %d %+v", code, convs.Items)
	}
	var thread []conversation.Conversation
	if code := r.p.admin(http.MethodGet, "/api/v1/conversations/"+convs.Items[0].ID.String()+"/thread", nil, &thread); code != http.StatusOK || len(thread) != 2 {
		t.Fatalf("thread doors: %d %d", code, len(thread))
	}
}

// Any dock that only triggers can have a mirror: an api dock with replies
// off, whose inbound arrives through the HTTP API.
func TestMirrorOfAnAPIDockWithRepliesOff(t *testing.T) {
	r := newMirrorRig(t)
	p := r.p
	api := p.apiDock("feed")
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+api.ID.String(), map[string]bool{"replies": false}, nil); code != http.StatusOK {
		t.Fatalf("replies off: %d", code)
	}
	r.setMirror(api)
	p.letIn(api)
	p.inject(api, "feed-1", "m1", "something happened")
	if n := r.slackPosts(); n != 1 {
		t.Fatalf("slack posts: %d", n)
	}
	if text, _ := r.sl.posted()[0]["text"].(string); text != "something happened" {
		t.Fatalf("mirror post: %v", r.sl.posted()[0])
	}
	// The next message of that chat is in a thread of two: nothing new.
	p.inject(api, "feed-1", "m2", "again")
	if n := r.slackPosts(); n != 1 {
		t.Fatalf("slack posts after a second message: %d", n)
	}
	// With replies on, the dock answers in place and has no mirror.
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+api.ID.String()+"/mirror", map[string]any{"mirror": nil}, nil); code != http.StatusOK {
		t.Fatalf("clear mirror: %d", code)
	}
}

// The mirror's rules: a dock with one can't turn replies on, a dock that
// replies can't have one, and removing the destination clears it.
func TestMirrorRules(t *testing.T) {
	r := newMirrorRig(t)
	p := r.p
	hookPath := "/api/v1/docks/" + r.hook.ID.String()

	var got dock.Dock
	if code := p.admin(http.MethodGet, hookPath, nil, &got); code != http.StatusOK || got.Mirror == nil ||
		got.Mirror.Dock != r.slack.ID || got.Mirror.Destination != "oncall" {
		t.Fatalf("dock shows its mirror: %d %+v", code, got.Mirror)
	}
	api := p.apiDock("feed")
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+api.ID.String()+"/mirror",
		map[string]any{"mirror": map[string]string{"dock_id": r.slack.ID.String(), "destination": "oncall"}}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("mirror on a dock that replies: %d", code)
	}
	if code := p.admin(http.MethodPut, hookPath+"/mirror",
		map[string]any{"mirror": map[string]string{"dock_id": r.hook.ID.String(), "destination": "oncall"}}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("a dock as its own mirror: %d", code)
	}
	if code := p.admin(http.MethodPut, hookPath+"/mirror",
		map[string]any{"mirror": map[string]string{"dock_id": r.slack.ID.String(), "destination": "nope"}}, nil); code != http.StatusNotFound {
		t.Fatalf("a missing destination: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+identity.NewDockID().String()+"/mirror", map[string]any{"mirror": nil}, nil); code != http.StatusNotFound {
		t.Fatalf("a missing dock: %d", code)
	}
	// Replies on (an api dock whose mirror is set while they are off).
	off := p.apiDock("quiet")
	p.admin(http.MethodPatch, "/api/v1/docks/"+off.ID.String(), map[string]bool{"replies": false}, nil)
	r.setMirror(off)
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+off.ID.String(), map[string]bool{"replies": true}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("replies on with a mirror: %d", code)
	}
	// The Slack dock stops being a place to post when its destination goes.
	if code := p.admin(http.MethodDelete, "/api/v1/docks/"+r.slack.ID.String()+"/destinations/oncall", nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove destination: %d", code)
	}
	for _, id := range []identity.DockID{r.hook.ID, off.ID} {
		var after dock.Dock // fresh: an omitted field leaves a reused one as it was
		if code := p.admin(http.MethodGet, "/api/v1/docks/"+id.String(), nil, &after); code != http.StatusOK || after.Mirror != nil {
			t.Fatalf("mirror after its destination went: %d %+v", code, after.Mirror)
		}
	}
	// And now replies can go on.
	if code := p.admin(http.MethodPatch, "/api/v1/docks/"+off.ID.String(), map[string]bool{"replies": true}, nil); code != http.StatusOK {
		t.Fatalf("replies on without a mirror: %d", code)
	}
}

// Migration 00053 moves a Datadog dock's mirror from datadog_docks to the
// dock itself and leaves docks without one alone.
func TestUpgradeMovesDatadogMirrorsToDocks(t *testing.T) {
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.MigrateTo(ctx, db.DB, 51); err != nil {
		t.Fatal(err)
	}
	org := config.DefaultOrganization
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	chat, mirrored, plain := identity.NewDockID(), identity.NewDockID(), identity.NewDockID()
	for id, ch := range map[identity.DockID]string{chat: "api", mirrored: "datadog", plain: "datadog"} {
		exec(`INSERT INTO docks (id, organization_id, channel, name, status) VALUES ($1, $2, $3, $4, 'linked')`, id, org, ch, ch+"-"+id.String()[:4])
	}
	exec(`INSERT INTO dock_destinations (organization_id, dock_id, name, target) VALUES ($1, $2, 'oncall', 'oncall-feed')`, org, chat)
	exec(`INSERT INTO datadog_docks (dock_id, organization_id, secret, mirror_dock, mirror_destination) VALUES ($1, $2, '\x01', $3, 'oncall')`, mirrored, org, chat)
	exec(`INSERT INTO datadog_docks (dock_id, organization_id, secret) VALUES ($1, $2, '\x02')`, plain, org)

	if err := bootstrap.MigrateTo(ctx, db.DB, 0); err != nil {
		t.Fatal(err)
	}
	type row struct {
		Dock *identity.DockID `db:"mirror_dock"`
		Name *string          `db:"mirror_destination"`
	}
	var got row
	if err := db.Get(&got, `SELECT mirror_dock, mirror_destination FROM docks WHERE id = $1`, mirrored); err != nil ||
		got.Dock == nil || *got.Dock != chat || got.Name == nil || *got.Name != "oncall" {
		t.Fatalf("mirrored dock: %+v %v", got, err)
	}
	got = row{}
	if err := db.Get(&got, `SELECT mirror_dock, mirror_destination FROM docks WHERE id = $1`, plain); err != nil || got.Dock != nil || got.Name != nil {
		t.Fatalf("dock without a mirror: %+v %v", got, err)
	}
	var left int
	if err := db.Get(&left, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'datadog_docks' AND column_name LIKE 'mirror%'`); err != nil || left != 0 {
		t.Fatalf("datadog_docks keeps mirror columns: %d %v", left, err)
	}
	// Removing the destination clears the mirror (the foreign key).
	exec(`DELETE FROM dock_destinations WHERE dock_id = $1`, chat)
	if err := db.Get(&got, `SELECT mirror_dock, mirror_destination FROM docks WHERE id = $1`, mirrored); err != nil || got.Dock != nil || got.Name != nil {
		t.Fatalf("mirror after its destination went: %+v %v", got, err)
	}
	// The table refuses a dock that mirrors to itself and a half pair.
	if _, err := db.Exec(`UPDATE docks SET mirror_dock = id, mirror_destination = 'x' WHERE id = $1`, plain); err == nil {
		t.Fatal("a dock may not mirror to itself")
	}
	if _, err := db.Exec(`UPDATE docks SET mirror_destination = 'x' WHERE id = $1`, plain); err == nil {
		t.Fatal("a mirror needs both columns")
	}
}
