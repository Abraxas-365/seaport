package conversation_test

import (
	"encoding/json"
	"testing"

	"github.com/Abraxas-365/seaport/internal/conversation"
)

// The web reads a customs.updated event as the Customs object itself
// ({policy, rules}), not wrapped in a "customs" key.
func TestCustomsEventIsFlat(t *testing.T) {
	raw, err := json.Marshal(conversation.CustomsEvent{Customs: conversation.Customs{Rules: []conversation.Rule{}}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["policy"]; !ok {
		t.Fatalf("payload %s has no top-level policy", raw)
	}
	if _, ok := m["rules"]; !ok {
		t.Fatalf("payload %s has no top-level rules", raw)
	}
	if _, ok := m["customs"]; ok {
		t.Fatalf("payload %s wraps the customs", raw)
	}
}
