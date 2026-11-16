package agentsvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/agent/agentsvc"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// sweepRepo is the part of agent.Repository the sweep uses: stale lists
// what the query would, rows are the takeovers SetTakeovers sees under the
// row lock (which may differ: a Listo landed in between), errs fails an
// agent's SetTakeovers.
type sweepRepo struct {
	agent.Repository
	stale  []agent.AgentRef
	cutoff time.Time
	rows   map[identity.AgentID]agent.Takeovers
	errs   map[identity.AgentID]error
}

func (r *sweepRepo) StaleTakeovers(_ context.Context, cutoff time.Time) ([]agent.AgentRef, error) {
	r.cutoff = cutoff
	return r.stale, nil
}

func (r *sweepRepo) SetTakeovers(_ context.Context, org identity.OrganizationID, id identity.AgentID, change func(agent.Takeovers) agent.Takeovers) (agent.Agent, error) {
	if err := r.errs[id]; err != nil {
		return agent.Agent{}, err
	}
	r.rows[id] = change(r.rows[id])
	return agent.Agent{ID: id, Organization: org, Name: "a-" + id.String()[:4], Takeovers: r.rows[id]}, nil
}

type wakeCall struct {
	org  identity.OrganizationID
	host string
	end  agent.TakeoverEnd
	in   identity.ConversationID
}

type assistant struct{ woken []wakeCall }

func (*assistant) Notify(context.Context, identity.OrganizationID, agent.Agent, agent.Takeover, string) (identity.ConversationID, error) {
	return identity.ConversationID{}, errors.New("not used")
}

func (a *assistant) Wake(_ context.Context, org identity.OrganizationID, _ agent.Agent, t agent.Takeover, end agent.TakeoverEnd, in identity.ConversationID) error {
	a.woken = append(a.woken, wakeCall{org, t.Host, end, in})
	return nil
}

type published struct {
	org  identity.OrganizationID
	typ  string
	data any
}

type recorder struct{ events []published }

func (r *recorder) Publish(org identity.OrganizationID, typ string, data any) {
	r.events = append(r.events, published{org, typ, data})
}

// TestSweepTakeovers: only takeovers past agent.TakeoverTTL *under the row
// lock* are dropped, each published as expired and woken once where the
// notice went (else the call's conversation; with neither, nobody is
// woken but it still counts); a Listo that landed after the list wins
// (nothing published, nobody woken); an agent deleted meanwhile is
// skipped; a failing agent doesn't stop the others and its error is
// returned.
func TestSweepTakeovers(t *testing.T) {
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	old, fresh := now.Add(-agent.TakeoverTTL-time.Minute), now.Add(-time.Minute)
	org1 := identity.MustParseOrganizationID("00000000-0000-0000-0000-0000000000a1")
	org2 := identity.MustParseOrganizationID("00000000-0000-0000-0000-0000000000a2")
	callConv, noticeConv := identity.NewConversationID(), identity.NewConversationID()
	mixed, raced, gone, broken, quiet := identity.NewAgentID(), identity.NewAgentID(), identity.NewAgentID(), identity.NewAgentID(), identity.NewAgentID()

	repo := &sweepRepo{
		stale: []agent.AgentRef{
			{Organization: org1, Agent: mixed},
			{Organization: org1, Agent: raced},
			{Organization: org1, Agent: gone},
			{Organization: org2, Agent: broken},
			{Organization: org2, Agent: quiet},
		},
		rows: map[identity.AgentID]agent.Takeovers{
			// One stale with a notice elsewhere, one stale told nowhere but
			// named in the call, one fresh: two go, one stays.
			mixed: {
				{Host: "old.example.com", Kind: agent.KindCaptcha, At: old, Conversation: &callConv, NotifiedIn: &noticeConv},
				{Host: "call.example.com", Kind: agent.KindLogin, At: old, Conversation: &callConv},
				{Host: "fresh.example.com", Kind: agent.KindLogin, At: fresh, Conversation: &callConv},
			},
			// Listed as stale, but a person pressed Listo before the lock:
			// only a fresh one is left.
			raced: {{Host: "new.example.com", Kind: agent.KindLogin, At: fresh}},
			// No conversation at all: dropped and counted, nobody woken.
			quiet: {{Host: "lost.example.com", Kind: agent.Kind2FA, At: old}},
		},
		errs: map[identity.AgentID]error{
			gone:   errx.NotFound("agent not found"),
			broken: errx.New("connection reset", errx.TypeInternal),
		},
	}
	events := &recorder{}
	helper := &assistant{}
	svc := agentsvc.New(repo, nil, events)
	svc.Bind(helper)

	n, err := svc.SweepTakeovers(context.Background(), now)
	if err == nil || !errx.IsType(err, errx.TypeInternal) {
		t.Fatalf("err = %v, want the broken agent's", err)
	}
	if n != 3 {
		t.Fatalf("expired %d, want 3", n)
	}
	if want := now.Add(-agent.TakeoverTTL); !repo.cutoff.Equal(want) {
		t.Fatalf("cutoff %v, want %v", repo.cutoff, want)
	}

	if l := repo.rows[mixed]; len(l) != 1 || l[0].Host != "fresh.example.com" {
		t.Fatalf("mixed left %+v", l)
	}
	if l := repo.rows[raced]; len(l) != 1 || l[0].Host != "new.example.com" {
		t.Fatalf("raced left %+v", l)
	}
	if l := repo.rows[quiet]; len(l) != 0 {
		t.Fatalf("quiet left %+v", l)
	}

	wantWake := []wakeCall{
		{org1, "old.example.com", agent.TakeoverExpired, noticeConv},
		{org1, "call.example.com", agent.TakeoverExpired, callConv},
	}
	if len(helper.woken) != len(wantWake) {
		t.Fatalf("woken %+v, want %+v", helper.woken, wantWake)
	}
	for i, w := range wantWake {
		if helper.woken[i] != w {
			t.Fatalf("wake %d = %+v, want %+v", i, helper.woken[i], w)
		}
	}

	// Per agent that lost any: agent.updated, then one expired trail each.
	var got []string
	for _, e := range events.events {
		switch d := e.data.(type) {
		case agent.Agent:
			got = append(got, e.typ+" "+d.ID.String())
		case agent.TakeoverEvent:
			if d.State != agent.TakeoverStateExpired || d.EndedBy != "" {
				t.Fatalf("trail %+v", d)
			}
			got = append(got, e.typ+" "+d.Host)
		default:
			t.Fatalf("published %T", e.data)
		}
	}
	want := []string{
		agent.EventUpdated + " " + mixed.String(),
		agent.EventTakeover + " old.example.com",
		agent.EventTakeover + " call.example.com",
		agent.EventUpdated + " " + quiet.String(),
		agent.EventTakeover + " lost.example.com",
	}
	if len(got) != len(want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Without an assistant (none bound yet) the sweep still drops and counts.
func TestSweepTakeoversWithoutAssistant(t *testing.T) {
	now := time.Now().UTC()
	id := identity.NewAgentID()
	org := identity.MustParseOrganizationID("00000000-0000-0000-0000-0000000000b1")
	conv := identity.NewConversationID()
	repo := &sweepRepo{
		stale: []agent.AgentRef{{Organization: org, Agent: id}},
		rows:  map[identity.AgentID]agent.Takeovers{id: {{Host: "x.example.com", Kind: agent.KindLogin, At: now.Add(-2 * agent.TakeoverTTL), Conversation: &conv}}},
	}
	n, err := agentsvc.New(repo, nil, nil).SweepTakeovers(context.Background(), now)
	if err != nil || n != 1 || len(repo.rows[id]) != 0 {
		t.Fatalf("n=%d err=%v left=%+v", n, err, repo.rows[id])
	}
}
