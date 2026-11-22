package hosting_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

func TestParseAlertWait(t *testing.T) {
	for in, want := range map[string]time.Duration{"": hosting.DefaultAlertWait, "1m": time.Minute, "90m": 90 * time.Minute, "24h": 24 * time.Hour} {
		if got, err := hosting.ParseAlertWait(in); err != nil || got != want {
			t.Errorf("ParseAlertWait(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"30s", "25h", "1m30s", "0", "-5m", "soon"} {
		if _, err := hosting.ParseAlertWait(in); err == nil {
			t.Errorf("ParseAlertWait(%q) passed", in)
		}
	}
	if got := hosting.FormatAlertWait(90 * time.Minute); got != "1h30m" {
		t.Errorf("FormatAlertWait = %q", got)
	}
}

func TestHostingPatchValidate(t *testing.T) {
	on := true
	dock := identity.NewDockID()
	ok := []hosting.HostingPatch{
		{Autoscale: &on},
		{Alerts: &hosting.AlertsPatch{Off: true}},
		{Alerts: &hosting.AlertsPatch{Dock: dock, Destination: "ops"}},
		{Autoscale: &on, Alerts: &hosting.AlertsPatch{Dock: dock, Destination: "ops", Wait: "1h"}},
	}
	for _, p := range ok {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	bad := []hosting.HostingPatch{
		{},
		{Alerts: &hosting.AlertsPatch{Destination: "ops"}},
		{Alerts: &hosting.AlertsPatch{Dock: dock}},
		{Alerts: &hosting.AlertsPatch{Dock: dock, Destination: "ops", Wait: "10s"}},
	}
	for _, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("%+v passed", p)
		}
	}
}

func TestAlertText(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	link := "https://seaport.example.com/agents/x#donde"
	base := hosting.Alert{AgentName: "pm", Since: now.Add(-25 * time.Minute)}
	cases := []struct {
		alert hosting.Alert
		want  []string
	}{
		{alert(base, hosting.AlertSlotFailed, "2", "the container exited", 0), []string{"pm-2 falló", "Motivo: the container exited", link}},
		{alert(base, hosting.AlertBuildFailed, "abc", "apt failed", 0), []string{"imagen de @pm no se pudo construir", "Motivo: apt failed"}},
		{alert(base, hosting.AlertBuildSlow, "abc", "", 0), []string{"lleva 25 min construyéndose"}},
		{alert(base, hosting.AlertWaiting, "", "", 1), []string{"una conversación lleva 25 min esperando a @pm"}},
		{alert(base, hosting.AlertWaiting, "", "", 3), []string{"3 conversaciones esperan a @pm", "desde hace 25 min"}},
	}
	for _, c := range cases {
		got := hosting.AlertText(c.alert, link, now)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.alert.Kind, got, w)
			}
		}
	}
	resolved := alert(base, hosting.AlertSlotFailed, "1", "", 0)
	at := now.Add(-time.Minute) // lasted 24 min, then 2 h 0 min
	resolved.ResolvedAt = &at
	if got := hosting.AlertText(resolved, link, now); !strings.HasPrefix(got, "Resuelto: la instancia pm-1") ||
		!strings.Contains(got, "Duró 24 min.") || strings.Contains(got, link) {
		t.Errorf("resolved: %q", got)
	}
	resolved.Kind, resolved.Since = hosting.AlertWaiting, at.Add(-2*time.Hour)
	if got := hosting.AlertText(resolved, "", now); !strings.Contains(got, "Duró 2 h.") {
		t.Errorf("resolved waiting: %q", got)
	}
	// No link when the port has no public URL.
	if got := hosting.AlertText(alert(base, hosting.AlertBuildSlow, "abc", "", 0), "", now); strings.Contains(got, "\n\n") {
		t.Errorf("an empty link left a blank line: %q", got)
	}
}

func alert(a hosting.Alert, kind hosting.AlertKind, key, detail string, waiting int) hosting.Alert {
	a.Kind, a.Key, a.Detail, a.Waiting = kind, key, detail, waiting
	return a
}
