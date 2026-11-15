package shipsvc

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Takeovers is the agent domain's Assistant (agent.Assistant): a browser
// takeover's notice goes to people as an ordinary outbound ship, and the
// agent is woken with a system inbound ship when a person ends it.
type Takeovers struct{ s *Service }

// Takeovers returns the service as an agent.Assistant.
func (s *Service) Takeovers() Takeovers { return Takeovers{s: s} }

var _ agent.Assistant = Takeovers{}

// Notify tells people. On the takeover's conversation
// (agent.AssistConversation): a reply there (Send's checks apply: the
// conversation must be allowed and the dock must take replies). Else
// where is a destination name: Open, at the first of the agent's docks
// that has it, as a thread of the takeover's conversation when the dock
// is the same (a Slack thread under the chat; elsewhere thread_of would
// link a conversation of another dock, which is the agent's call, not
// ours). The agent signs both (Agent set), so an agent off every dock
// tells nobody.
func (t Takeovers) Notify(ctx context.Context, org identity.OrganizationID, a agent.Agent, tk agent.Takeover, where string) (identity.ConversationID, error) {
	txt := t.notice(a, tk)
	if where == agent.AssistConversation {
		if tk.Conversation == nil {
			return identity.ConversationID{}, errx.Business("the takeover names no conversation")
		}
		out, err := t.s.Send(ctx, org, ship.Outbound{Conversation: *tk.Conversation, Text: txt, Agent: &a.ID})
		if err != nil {
			return identity.ConversationID{}, err
		}
		return out.Conversation, nil
	}
	if t.s.docks == nil {
		return identity.ConversationID{}, errx.Business("this port has no destinations")
	}
	for _, d := range a.Docks {
		dest, err := t.s.docks.Destination(ctx, org, d.Dock, where)
		if err != nil {
			if errx.IsType(err, errx.TypeNotFound) {
				continue
			}
			return identity.ConversationID{}, errx.Wrap(err, "find destination", errx.TypeInternal)
		}
		open := ship.Open{Dock: d.Dock, Destination: dest.Name, Text: txt, Agent: &a.ID}
		if tk.Conversation != nil {
			if conv, err := t.s.convs.Find(ctx, org, *tk.Conversation); err == nil && conv.Dock == d.Dock {
				open.ThreadOf = conv.ID
			}
		}
		out, err := t.s.Open(ctx, org, open)
		if err != nil {
			return identity.ConversationID{}, err
		}
		return out.Conversation, nil
	}
	return identity.ConversationID{}, errx.NotFound("destination " + strconv.Quote(where) + " is on none of the agent's docks")
}

// notice is what people read. Spanish, like the approval card: the
// reader is a person on the channel.
func (t Takeovers) notice(a agent.Agent, tk agent.Takeover) string {
	var b strings.Builder
	fmt.Fprintf(&b, "El agente *%s* necesita que una persona %s en %s.", a.Name, takeoverAction(tk), tk.Host)
	if tk.Note != "" {
		fmt.Fprintf(&b, " Qué hacía: %s.", strings.TrimRight(tk.Note, "."))
	}
	if t.s.publicURL != "" {
		fmt.Fprintf(&b, "\n\nAbre su navegador: %s/agents/%s/hosting\nCuando termines, pulsa Listo.", t.s.publicURL, a.ID)
	} else {
		b.WriteString("\n\nAbre su navegador en la app web (Agentes → el agente → Navegador). Cuando termines, pulsa Listo.")
	}
	return b.String()
}

func takeoverAction(tk agent.Takeover) string {
	switch tk.Kind {
	case agent.KindCaptcha:
		return "resuelva un captcha"
	case agent.Kind2FA:
		return "introduzca un código de verificación"
	case agent.KindOther:
		return "intervenga"
	default:
		return "inicie sesión"
	}
}

// Wake tells the agent, in the conversation its notice went to, how the
// takeover ended (a person is done, or nobody came): a system inbound
// ship through Receive (customs, routing and threads apply), addressed to
// it by @mention. An agent no longer on the dock's team is not woken (the
// mention would route elsewhere); the ship's external id makes a repeat
// one ship.
func (t Takeovers) Wake(ctx context.Context, org identity.OrganizationID, a agent.Agent, tk agent.Takeover, end agent.TakeoverEnd, in identity.ConversationID) error {
	conv, err := t.s.convs.Find(ctx, org, in)
	if err != nil {
		return errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	if err := t.s.onDock(ctx, org, a.ID, conv.Dock); err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			slog.Info("takeover ended; its agent left the dock's team, not woken", "agent", a.Name, "host", tk.Host)
			return nil
		}
		return err
	}
	now := t.s.now()
	st, _, err := t.s.Receive(ctx, org, ship.Inbound{
		Dock: conv.Dock, Channel: conv.Channel, ChatID: conv.ExternalID, ChatKind: conv.Kind, ChatTitle: conv.Title,
		ExternalID: "takeover-" + a.ID.String() + "-" + tk.Host + "-" + string(end) + "-" + strconv.FormatInt(now.Unix(), 10),
		SenderID:   ship.SystemBrowser, SenderName: "Seaport",
		Text:   wakeText(a, tk, end),
		SentAt: now, Addressed: true,
	})
	if err != nil {
		return err
	}
	if st.Status == ship.StatusHeld {
		slog.Info("takeover wake held", "agent", a.Name, "host", tk.Host, "reason", st.HoldReason)
	}
	return nil
}

// wakeText is the system message that wakes the agent.
func wakeText(a agent.Agent, tk agent.Takeover, end agent.TakeoverEnd) string {
	if end == agent.TakeoverExpired {
		return fmt.Sprintf("@%s [takeover %s] nobody came within %s (%s) at %s. Go on without it, or ask again.",
			a.Name, tk.Host, agent.TakeoverTTL, tk.Kind, tk.Host)
	}
	return fmt.Sprintf("@%s [takeover %s] a person is done (%s) at %s. Go on in the browser.", a.Name, tk.Host, tk.Kind, tk.Host)
}
