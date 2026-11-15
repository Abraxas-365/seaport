package dockdatadog

import (
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/text"
)

// FuzzParseRender: any body is refused or parsed, and a parsed payload
// renders to a message within the limits the conversation domain checks.
func FuzzParseRender(f *testing.F) {
	for _, s := range []string{
		`{"id": 9001, "alert_id": "4242", "transition": "Triggered", "title": "CPU high", "status": "CPU > 90%", "scope": "host:web-1", "tags": "env:prod, service:web,env:prod,,team:platform", "message": "look", "link": "https://app.datadoghq.com/event/event?id=9001", "priority": "P1"}`,
		`{"alert_id": 7, "transition": "Recovered", "title": "$ALERT_TITLE", "scope": "$ALERT_SCOPE", "tags": "$TAGS", "link": "http://evil"}`,
		`{"alert_id": "$ALERT_ID"}`, `{"alert_id": null}`, `{"alert_id": 1e400}`, `{"alert_id": {"a":1}}`, `{"alert_id": ""}`, `[]`, ``, ` {`,
		`{"alert_id": "1", "tags": "a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p,q,r,s,t", "scope": "x\u0000y"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		p, err := Parse(body)
		if err != nil {
			return
		}
		m := Render(p, time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC))
		if m.ChatID == "" || m.ExternalID == "" || m.Text == "" || m.ChatTitle == "" {
			t.Fatalf("empty field: %+v", m)
		}
		if utf8.RuneCountInString(m.ChatTitle) > text.MaxName || utf8.RuneCountInString(m.ExternalID) > conversation.MaxExternalID ||
			utf8.RuneCountInString(m.Text) > maxText || len(m.Handles) > conversation.MaxHandles {
			t.Fatalf("out of limits: title %d, external %d, text %d, handles %d", utf8.RuneCountInString(m.ChatTitle),
				utf8.RuneCountInString(m.ExternalID), utf8.RuneCountInString(m.Text), len(m.Handles))
		}
	})
}
