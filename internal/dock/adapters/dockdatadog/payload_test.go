package dockdatadog

import (
	"strings"
	"testing"
	"time"
)

func TestParseAndRender(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	body := `{"id": 9001, "alert_id": "4242", "transition": "Triggered", "title": "CPU high", "status": "CPU > 90%",
		"scope": "host:web-1", "tags": "env:prod, service:web,env:prod,,team:platform", "message": "look", "link": "https://app.datadoghq.com/event/event?id=9001", "priority": "P1"}`
	p, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	m := Render(p, now)
	if m.ChatID != "monitor/4242/host:web-1" || m.ExternalID != "event-9001" || m.Kind != KindTriggered || m.ChatTitle != "CPU high · host:web-1" {
		t.Fatalf("message: %+v", m)
	}
	want := []string{"monitor/4242/host:web-1", "monitor:4242", "scope:host:web-1", "tag:env:prod", "tag:service:web", "tag:team:platform"}
	if strings.Join(m.Handles, " ") != strings.Join(want, " ") {
		t.Fatalf("handles: %v", m.Handles)
	}
	for _, s := range []string{"**Triggered** · CPU high", "CPU > 90%", "Scope: host:web-1", "Priority: P1", "Tags: env:prod, service:web, team:platform", "look", "https://app.datadoghq.com/event/event?id=9001"} {
		if !strings.Contains(m.Text, s) {
			t.Fatalf("text lacks %q:\n%s", s, m.Text)
		}
	}

	// Unfilled Datadog variables read as absent; no id makes one from the
	// alert and the minute; an empty scope is "*".
	p, err = Parse([]byte(`{"alert_id": 7, "transition": "Recovered", "title": "$ALERT_TITLE", "scope": "$ALERT_SCOPE", "tags": "$TAGS", "link": "http://evil"}`))
	if err != nil {
		t.Fatal(err)
	}
	m = Render(p, now)
	if m.ChatID != "monitor/7/*" || m.ChatTitle != "Monitor 7" || m.ExternalID != "event-7-*-recovered-202601020304" || len(m.Handles) != 2 ||
		strings.Contains(m.Text, "evil") || strings.Contains(m.Text, "Scope") {
		t.Fatalf("defaults: %+v", m)
	}

	for name, body := range map[string]string{"array": `[]`, "text": `hello`, "no monitor": `{"title": "x"}`, "unfilled": `{"alert_id": "$ALERT_ID"}`} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestKindOf(t *testing.T) {
	for in, want := range map[string]Kind{"Triggered": KindTriggered, "recovered": KindRecovered, "Re-notify": KindRenotify, "Warn": KindWarn, "No Data": KindNoData, "": KindOther} {
		if got := KindOf(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestProof(t *testing.T) {
	if !validToken("s3cret", " s3cret ") || validToken("s3cret", "") || validToken("s3cret", "S3cret") {
		t.Fatal("token")
	}
	if !validBearer("s3cret", "bearer s3cret") || validBearer("s3cret", "s3cret") || validBearer("s3cret", "Basic s3cret") {
		t.Fatal("bearer")
	}
}
