package hostingsvc

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// alertsCfg is how hosted agents' alerts reach people (BindAlerts).
type alertsCfg struct {
	// Alerter sends them (nil: alerts can't be turned on).
	Alerter hosting.Alerter
	// PublicURL makes the link to the agent's page in a notice ("": none).
	PublicURL string
	// Every overrides hosting.AlertEvery (tests).
	Every time.Duration
}

// alerts runs the supervisor's alert turn.
type alerts struct {
	cfg alertsCfg
	// sending: a turn is running in this process; lookedAt: when the last
	// one started.
	sending  atomic.Bool
	lookedAt time.Time
}

// BindAlerts wires the alerts' sender (bootstrap: the dock and ship
// services, built after hosting); publicURL links notices to the agent's
// page, every overrides hosting.AlertEvery (tests; 0: the default).
func (s *Service) BindAlerts(a hosting.Alerter, publicURL string, every time.Duration) {
	s.builds.mu.Lock()
	s.alerts.cfg = alertsCfg{Alerter: a, PublicURL: publicURL, Every: every}
	s.builds.mu.Unlock()
}

func (s *Service) alerter() (hosting.Alerter, alertsCfg) {
	s.builds.mu.Lock()
	defer s.builds.mu.Unlock()
	return s.alerts.cfg.Alerter, s.alerts.cfg
}

// checkAlerts refuses alerts the port can't send: no sender, or a
// destination missing or on a dock that can't open conversations now.
func (s *Service) checkAlerts(ctx context.Context, org identity.OrganizationID, p *hosting.AlertsPatch) error {
	if p == nil || p.Off {
		return nil
	}
	a, _ := s.alerter()
	if a == nil {
		return errx.Business("alerts are off on this port")
	}
	return a.Check(ctx, org, p.Dock, p.Destination)
}

// sendAlerts is the alert turn of a supervisor round (see hosting/
// alert.go): at most every AlertEvery per process, in the background, one
// at a time. Raised alerts open the destination; resolved ones answer in
// the conversation the notice went to (or open it again).
func (s *Service) sendAlerts(now time.Time) {
	a, cfg := s.alerter()
	if a == nil {
		return
	}
	every := cfg.Every
	if every <= 0 {
		every = hosting.AlertEvery
	}
	s.builds.mu.Lock()
	due := s.alerts.lookedAt.IsZero() || now.Sub(s.alerts.lookedAt) >= every
	if due {
		s.alerts.lookedAt = now
	}
	s.builds.mu.Unlock()
	if !due || !s.alerts.sending.CompareAndSwap(false, true) {
		return
	}
	if s.builds.ctx.Err() != nil {
		s.alerts.sending.Store(false)
		return
	}
	s.background(func(ctx context.Context) {
		defer s.alerts.sending.Store(false)
		due, err := s.repo.Alerts(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("alerts", "err", err)
			}
			return
		}
		for _, al := range due {
			if ctx.Err() != nil {
				return
			}
			s.sendAlert(ctx, a, cfg, al)
		}
	})
}

// sendAlert sends one notice and records how it went.
func (s *Service) sendAlert(ctx context.Context, a hosting.Alerter, cfg alertsCfg, al hosting.Alert) {
	link := ""
	if cfg.PublicURL != "" {
		link = cfg.PublicURL + "/agents/" + al.Agent.String() + "#donde"
	}
	text := hosting.AlertText(al, link, time.Now())
	sent := hosting.AlertSent{ID: al.ID, Resolved: al.ResolvedAt != nil}
	var err error
	if sent.Resolved && al.Conversation != nil {
		sent.Conversation = *al.Conversation
		if err = a.Reply(ctx, al.Organization, *al.Conversation, text); err != nil {
			// The conversation may be gone (or its dock can't reply now):
			// tell the destination instead.
			sent.Conversation, err = a.Open(ctx, al.Organization, al.Dock, al.Destination, text)
		}
	} else {
		sent.Conversation, err = a.Open(ctx, al.Organization, al.Dock, al.Destination, text)
	}
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down: the claim lapses and it is tried again
		}
		sent.Err = alertError(err)
		slog.Warn("send alert", "org", al.Organization, "agent", al.Agent, "kind", al.Kind, "err", err)
	}
	changed, err := s.repo.AlertSent(ctx, al.Organization, sent)
	if err != nil {
		if ctx.Err() == nil && !errx.IsType(err, errx.TypeNotFound) {
			slog.Warn("record alert", "err", err)
		}
		return
	}
	if changed {
		if h, err := s.repo.Hosted(ctx, al.Organization, al.Agent); err == nil {
			s.events.Publish(al.Organization, hosting.EventSlotsUpdated, h)
		}
	}
}

// alertError is what the owner reads of a failed send: a typed error's
// message (the channel's own answer too: External), else a generic one.
func alertError(err error) string {
	var e *errx.Error
	if errx.As(err, &e) && e.Type != errx.TypeInternal && e.HTTPStatus != 0 {
		return e.Message
	}
	return "the dock couldn't send the notice"
}
