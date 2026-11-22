package hosting

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Alerts: the owner of a hosted agent names a dock destination (a WhatsApp
// chat, a Slack channel: dock.Destination) and the supervisor tells it
// when something needs a person, and again when it clears. Each condition
// (AlertKind + key) is one alert, open until it clears, so a slot retrying
// on its own or a queue that keeps waiting sends nothing new.
const (
	// AlertEvery is how often a process looks for alerts to raise, resolve
	// and send.
	AlertEvery = 30 * time.Second
	// AlertRetry is how long a notice that couldn't be sent (or whose send
	// is in progress in some process) waits before it is tried again.
	AlertRetry = 5 * time.Minute
	// BuildSlow raises AlertBuildSlow: the agent's wanted build has been
	// building this long.
	BuildSlow = 45 * time.Minute
	// DefaultAlertWait raises AlertWaiting when a conversation has waited
	// this long for the agent; Alerts.Wait changes it, between MinAlertWait
	// and MaxAlertWait.
	DefaultAlertWait = 10 * time.Minute
	MinAlertWait     = time.Minute
	MaxAlertWait     = 24 * time.Hour
	// AlertKeep is how long a resolved alert is kept.
	AlertKeep = 30 * 24 * time.Hour
)

// AlertKind is what an alert is about.
type AlertKind string

const (
	// AlertSlotFailed: a slot failed (key: its index). Open while it is
	// failed or starting again; resolved once it runs or is stopped.
	AlertSlotFailed AlertKind = "slot_failed"
	// AlertBuildFailed: the image the agent's config needs failed to build
	// (key: the build's digest). Open while that build is failed or being
	// built again; resolved once it is ready or the agent needs another.
	AlertBuildFailed AlertKind = "build_failed"
	// AlertBuildSlow: that build has been building BuildSlow (key: its
	// digest). Resolved once it ends (ready, or failed: AlertBuildFailed).
	AlertBuildSlow AlertKind = "build_slow"
	// AlertWaiting: a conversation has waited the alert wait for the agent
	// (key ''). Resolved once none waits.
	AlertWaiting AlertKind = "waiting"
)

// Alerts is where a hosted agent's alerts go (Hosted.Alerts; nil: off).
type Alerts struct {
	DockID      identity.DockID `json:"dock_id"`
	Destination string          `json:"destination"`
	// Wait raises AlertWaiting ("10m", "1h"; DefaultAlertWait).
	Wait string `json:"wait"`
	// Error is why the last notice couldn't be sent ("" once one went).
	Error string `json:"error"`
}

// AlertsPatch sets where the agent's alerts go; Off turns them off.
type AlertsPatch struct {
	Off         bool
	Dock        identity.DockID
	Destination string
	// Wait: "" is DefaultAlertWait.
	Wait string
}

func (p AlertsPatch) Validate() error {
	if p.Off {
		return nil
	}
	if p.Dock.IsZero() {
		return errx.InvalidField("alerts.dock_id", "alerts.dock_id is required")
	}
	if p.Destination == "" {
		return errx.InvalidField("alerts.destination", "alerts.destination is required (one of the dock's destinations)")
	}
	_, err := ParseAlertWait(p.Wait)
	return err
}

// ParseAlertWait reads Alerts.Wait: "" is DefaultAlertWait; whole minutes
// between MinAlertWait and MaxAlertWait.
func ParseAlertWait(v string) (time.Duration, error) {
	if v == "" {
		return DefaultAlertWait, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < MinAlertWait || d > MaxAlertWait || d%time.Minute != 0 {
		return 0, errx.InvalidField("alerts.wait", "alerts.wait must be whole minutes between 1m and 24h (\"10m\", \"1h\")")
	}
	return d, nil
}

// FormatAlertWait writes d as Alerts.Wait ("10m", "1h30m").
func FormatAlertWait(d time.Duration) string { return formatIdleStop(d) }

// Alert is a condition raised for a hosted agent, as the supervisor sends
// it (Repository.Alerts): its notice, or its "resolved" notice once
// ResolvedAt is set.
type Alert struct {
	ID           int64                   `db:"id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Agent        identity.AgentID        `db:"agent_id"`
	// AgentName is the agent's @name.
	AgentName string    `db:"agent_name"`
	Kind      AlertKind `db:"kind"`
	Key       string    `db:"key"`
	// Detail: the failed slot's or build's reason (no secrets).
	Detail string `db:"detail"`
	// Waiting counts the conversations waiting when it was raised.
	Waiting int `db:"waiting"`
	// Since is when the condition began (the slot failed, the build
	// started, the oldest conversation began waiting).
	Since      time.Time  `db:"since"`
	RaisedAt   time.Time  `db:"raised_at"`
	ResolvedAt *time.Time `db:"resolved_at"`
	// Dock and Destination: where it goes (the agent's Alerts).
	Dock        identity.DockID `db:"dock_id"`
	Destination string          `db:"destination"`
	// Conversation is where its notice went (resolved notices answer
	// there; nil: open the destination again).
	Conversation *identity.ConversationID `db:"conversation_id"`
}

// AlertSent records how sending an alert's notice went: Conversation is
// where it went; Err is why it couldn't ("" when it went).
type AlertSent struct {
	ID           int64
	Resolved     bool
	Conversation identity.ConversationID
	Err          string
}

// AlertText is an alert's notice in Spanish, the way people read it in a
// chat: what happened to which agent, since when, and where to look
// (link: the agent's page on the web, "" when the port has no public URL).
func AlertText(a Alert, link string, now time.Time) string {
	var b strings.Builder
	name := "@" + a.AgentName
	if a.ResolvedAt != nil {
		switch a.Kind {
		case AlertSlotFailed:
			fmt.Fprintf(&b, "Resuelto: la instancia %s ya no está fallando.", SlotName(a.AgentName, slotOfKey(a.Key)))
		case AlertBuildFailed:
			fmt.Fprintf(&b, "Resuelto: la imagen de %s ya no está fallando.", name)
		case AlertBuildSlow:
			fmt.Fprintf(&b, "Resuelto: terminó la construcción de la imagen de %s.", name)
		case AlertWaiting:
			fmt.Fprintf(&b, "Resuelto: ya no hay conversaciones esperando a %s.", name)
		default:
			fmt.Fprintf(&b, "Resuelto: aviso de %s.", name)
		}
		fmt.Fprintf(&b, " Duró %s.", ago(a.ResolvedAt.Sub(a.Since)))
		return b.String()
	}
	switch a.Kind {
	case AlertSlotFailed:
		fmt.Fprintf(&b, "Aviso: la instancia %s falló.", SlotName(a.AgentName, slotOfKey(a.Key)))
		if a.Detail != "" {
			fmt.Fprintf(&b, "\nMotivo: %s", a.Detail)
		}
		b.WriteString("\nSeaport la vuelve a arrancar si hay trabajo; si sigue fallando, revisa su registro.")
	case AlertBuildFailed:
		fmt.Fprintf(&b, "Aviso: la imagen de %s no se pudo construir.", name)
		if a.Detail != "" {
			fmt.Fprintf(&b, "\nMotivo: %s", a.Detail)
		}
		b.WriteString("\nSus instancias siguen con la imagen anterior si la tienen. Revisa el registro de la construcción.")
	case AlertBuildSlow:
		fmt.Fprintf(&b, "Aviso: la imagen de %s lleva %s construyéndose.", name, ago(now.Sub(a.Since)))
	case AlertWaiting:
		if a.Waiting == 1 {
			fmt.Fprintf(&b, "Aviso: una conversación lleva %s esperando a %s.", ago(now.Sub(a.Since)), name)
		} else {
			fmt.Fprintf(&b, "Aviso: %d conversaciones esperan a %s; la más antigua, desde hace %s.", a.Waiting, name, ago(now.Sub(a.Since)))
		}
	default:
		fmt.Fprintf(&b, "Aviso de %s.", name)
	}
	if link != "" {
		fmt.Fprintf(&b, "\n\n%s", link)
	}
	return b.String()
}

// ago writes d in Spanish ("5 min", "2 h 10 min", "menos de un minuto").
func ago(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1:
		return "menos de un minuto"
	case m < 60:
		return strconv.Itoa(m) + " min"
	case m%60 == 0:
		return strconv.Itoa(m/60) + " h"
	}
	return strconv.Itoa(m/60) + " h " + strconv.Itoa(m%60) + " min"
}

func slotOfKey(key string) int {
	n, _ := strconv.Atoi(key)
	return n
}
