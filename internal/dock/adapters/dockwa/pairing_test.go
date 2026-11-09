package dockwa_test

import (
	"bytes"
	"context"
	"testing"
)

// The pairing code is stored sealed and bound to its dock: any process
// reads it, a copy on another dock doesn't open, and "" clears it.
func TestPairingIsSealedPerDock(t *testing.T) {
	db, _ := testDB(t)
	ctx := context.Background()
	st := newStore(t, db, testKey)
	a, b := addDock(t, db, ""), addDock(t, db, "")
	const code = "2@pairing-code"

	if got, err := st.Pairing(ctx, a); err != nil || got != "" {
		t.Fatalf("no code yet: %q %v", got, err)
	}
	if err := st.SetPairing(ctx, org, a, code); err != nil {
		t.Fatal(err)
	}
	if got, err := newStore(t, db, testKey).Pairing(ctx, a); err != nil || got != code {
		t.Fatalf("another process reads it: %q %v", got, err)
	}
	var raw []byte
	if err := db.Get(&raw, `SELECT code FROM wa_pairings WHERE dock_id = $1`, a); err != nil || bytes.Contains(raw, []byte(code)) {
		t.Fatalf("stored in clear (%v)", err)
	}
	if _, err := db.Exec(`INSERT INTO wa_pairings (dock_id, organization_id, code) SELECT $2, organization_id, code FROM wa_pairings WHERE dock_id = $1`, a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pairing(ctx, b); err == nil {
		t.Fatal("a code copied to another dock opened")
	}
	if err := st.SetPairing(ctx, org, a, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Pairing(ctx, a); err != nil || got != "" {
		t.Fatalf("cleared: %q %v", got, err)
	}
}
