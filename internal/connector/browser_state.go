package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// jobTable is a session's running jobs (deliveries being worked) under one
// mutex, with what the frame loop keeps alongside: whether a person is in
// the slot's browser (viewer), and the detector's last ask per host.
type jobTable struct {
	mu   sync.Mutex
	jobs map[identity.DeliveryID]*job
	// started orders the jobs: the newest running one is the conversation
	// a detected takeover is told in.
	started map[identity.DeliveryID]time.Time
	// viewerOn: a person is in the slot's browser (browser frames).
	viewerOn bool
	// asked is when the detector last asked a takeover per host.
	asked map[string]time.Time
}

func newJobTable() *jobTable {
	return &jobTable{jobs: map[identity.DeliveryID]*job{}, started: map[identity.DeliveryID]time.Time{}, asked: map[string]time.Time{}}
}

// add registers a new job, whose prompt starts with the browser's state.
func (t *jobTable) add(j *job, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j.prelude = viewerPrelude(t.viewerOn)
	t.jobs[j.d.ID] = j
	t.started[j.d.ID] = now
}

func (t *jobTable) remove(id identity.DeliveryID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.jobs, id)
	delete(t.started, id)
}

func (t *jobTable) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.jobs)
}

func (t *jobTable) get(id identity.DeliveryID) *job {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.jobs[id]
}

func (t *jobTable) running() []*job {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*job, 0, len(t.jobs))
	for _, j := range t.jobs {
		out = append(out, j)
	}
	return out
}

// viewer records the viewer's state; it says whether it changed and
// returns the running jobs to tell.
func (t *jobTable) viewer(on bool) (changed bool, running []*job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	changed = t.viewerOn != on
	t.viewerOn = on
	for _, j := range t.jobs {
		running = append(running, j)
	}
	return changed, running
}

// detected decides what to do with a page the detector flagged: nothing
// while a person is in the viewer (they are on it), on an ignored host or
// for a host asked within agent.DetectEvery; else it records the ask and
// returns the running jobs and the conversation of the newest one (nil
// when none runs).
func (t *jobTable) detected(h pageHit, kit *agent.Kit, now time.Time) (ask bool, conv *identity.ConversationID, running []*job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.viewerOn || kit == nil || agent.HostIgnored(h.Host, kit.BrowserIgnoreHosts) {
		return false, nil, nil
	}
	if last, ok := t.asked[h.Host]; ok && now.Sub(last) < agent.DetectEvery {
		return false, nil, nil
	}
	t.asked[h.Host] = now
	var newest *job
	for id, j := range t.jobs {
		running = append(running, j)
		if newest == nil || t.started[id].After(t.started[newest.d.ID]) {
			newest = j
		}
	}
	if newest != nil {
		c := newest.d.Conversation.ID
		conv = &c
	}
	return true, conv, running
}

// detector is the connector's side of a detected page: it asks the port
// for the takeover and tells the running turns.
type detector struct {
	cfg  Config
	kit  *agent.Kit
	jobs *jobTable
	// post calls POST /crew/takeovers (tests swap it).
	post func(ctx context.Context, req crew.TakeoverRequest) (crew.TakeoverAnswer, error)
}

func newDetector(cfg Config, kit *agent.Kit, jobs *jobTable) *detector {
	d := &detector{cfg: cfg, kit: kit, jobs: jobs}
	d.post = d.callTakeovers
	return d
}

// hit handles one flagged page.
func (d *detector) hit(ctx context.Context, h pageHit) {
	ask, conv, running := d.jobs.detected(h, d.kit, time.Now())
	if !ask {
		return
	}
	note := "the connector's page detector saw a page only a person can pass"
	ans, err := d.post(ctx, crew.TakeoverRequest{Host: h.Host, Kind: string(h.Kind), Note: note, Detected: true, Path: h.Path, ConversationID: conv})
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("browser detector could not ask the takeover", "host", h.Host, "kind", h.Kind, "err", err)
		}
		return
	}
	slog.Info("browser detector asked a takeover", "host", h.Host, "kind", h.Kind, "path", h.Path, "notified", ans.Notified)
	for _, j := range running {
		j.note(detectNote(h, ans.Notified))
	}
}

// detectNote is steered into the running turns when the detector asked a
// takeover: the agent must not try to pass the page itself.
func detectNote(h pageHit, notified bool) string {
	var b strings.Builder
	b.WriteString("[seaport] Your browser is on a page at " + h.Host + h.Path + " that needs a person: ")
	switch h.Kind {
	case agent.KindCaptcha:
		b.WriteString("a captcha or challenge")
	case agent.Kind2FA:
		b.WriteString("a verification code")
	default:
		b.WriteString("a sign-in")
	}
	b.WriteString(". Seaport recorded a takeover for it")
	if notified {
		b.WriteString(" and told a person, who will do it in the viewer; you will get a system message when they are done")
	} else {
		b.WriteString("; nobody is configured to be told, so tell the people you work with yourself (the web's Navegador card has the viewer)")
	}
	b.WriteString(". Never guess or type credentials or codes. Go on with what doesn't need that page, or wait.")
	return b.String()
}

// callTakeovers is POST /crew/takeovers as the connector's agent.
func (d *detector) callTakeovers(ctx context.Context, in crew.TakeoverRequest) (crew.TakeoverAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.cfg.Server, "/")+crew.TakeoversPath, bytes.NewReader(body))
	if err != nil {
		return crew.TakeoverAnswer{}, errx.Wrap(err, "takeover request", errx.TypeValidation)
	}
	req.Header = authHeader(d.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return crew.TakeoverAnswer{}, errx.Wrap(err, "call the port", errx.TypeExternal)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return crew.TakeoverAnswer{}, errx.External("takeover: " + portMessage(raw, resp.Status))
	}
	var ans crew.TakeoverAnswer
	if err := json.Unmarshal(raw, &ans); err != nil {
		return crew.TakeoverAnswer{}, errx.External("the port's answer does not decode")
	}
	return ans, nil
}
