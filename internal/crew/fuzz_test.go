package crew_test

import (
	"encoding/json"
	"testing"

	"github.com/Abraxas-365/seaport/internal/crew"
)

// FuzzDecodeFrame: a connector's frame is untrusted JSON. Decoding never
// panics, a decoded frame's validators never panic, and re-encoding a
// frame decodes to the same JSON again.
func FuzzDecodeFrame(f *testing.F) {
	for _, s := range []string{
		`{"type":"hello","agent":"rness","as":"default","concurrency":4,"steer":true,"profile_version":3}`,
		`{"type":"welcome","mcp_url":"http://127.0.0.1:8080/mcp"}`,
		`{"type":"ack","delivery_id":"11111111-1111-1111-1111-111111111111","ship_ids":["22222222-2222-2222-2222-222222222222"]}`,
		`{"type":"activity","delivery_id":"11111111-1111-1111-1111-111111111111","items":[{"kind":"tool","tool":"t1","title":"Read","tool_kind":"read","status":"in_progress"},{"kind":"thought","text":"hm"}]}`,
		`{"type":"reply","conversation_id":"33333333-3333-3333-3333-333333333333","text":"hi"}`,
		`{"type":"delivery","delivery":{"id":"x","ships":[{}]}}`, `{"type":"error","message":"boom"}`, `{"type":"drain"}`,
		`{"type":"ack","delivery_id":"not-a-uuid"}`, `{"type":1}`, `[]`, `null`, ``, `{`, `{"items":[{"kind":"tool","tool":""}]}`,
		`{"concurrency":-1,"profile_version":-5,"as":"Not Valid!"}`, `{"ship_ids":null,"items":null}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		fr, err := crew.Decode(raw)
		if err != nil {
			return
		}
		_ = crew.Hello{Agent: fr.Agent, As: fr.As, Concurrency: fr.Concurrency, Steer: fr.Steer, ProfileVersion: fr.ProfileVersion}.Validate()
		_ = crew.Activity{Items: fr.Items}.Validate()
		_ = crew.Hello{Concurrency: fr.Concurrency}.EffectiveConcurrency()
		out, err := json.Marshal(fr)
		if err != nil {
			return // e.g. a number out of range of a type it decoded into
		}
		again, err := crew.Decode(out)
		if err != nil {
			t.Fatalf("a frame we encoded does not decode: %v\n%s", err, out)
		}
		out2, err := json.Marshal(again)
		if err != nil || string(out2) != string(out) {
			t.Fatalf("frame does not round trip:\n%s\n%s", out, out2)
		}
	})
}
