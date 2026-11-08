package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// TestSecretKey: SEAPORT_SECRET_KEY wins; otherwise the key file is read,
// or generated (0600) — but never while sealed WhatsApp devices exist,
// since a new key could not open them.
func TestSecretKey(t *testing.T) {
	startPort(t) // wiped and migrated database
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	file := filepath.Join(t.TempDir(), "conf", "secret.key")

	env := "Sk3Vt9xLm2Pz7Rw4Ys8Bn1Hc6Jd0Fg5Aq"
	if key, err := bootstrap.SecretKey(ctx, config.Server{SecretKey: env, SecretKeyFile: file}, db); err != nil || key != env {
		t.Fatalf("env key: %q %v", key, err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a key file was written although SEAPORT_SECRET_KEY is set")
	}
	if _, err := bootstrap.SecretKey(ctx, config.Server{SecretKey: strings.Repeat("ab", 20)}, db); err == nil {
		t.Fatal("placeholder SEAPORT_SECRET_KEY accepted")
	}

	cfg := config.Server{SecretKeyFile: file}
	generated, err := bootstrap.SecretKey(ctx, cfg, db)
	if err != nil || config.CheckSecretKey(generated) != nil {
		t.Fatalf("generated: %q %v", generated, err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	if again, err := bootstrap.SecretKey(ctx, cfg, db); err != nil || again != generated {
		t.Fatalf("second start: %q %v", again, err)
	}

	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.SecretKey(ctx, cfg, db); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("world-readable key file: %v", err)
	}

	// A sealed device exists and the file is gone: refuse, don't re-key.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	dock, org := identity.NewDockID(), config.DefaultOrganization
	if _, err := db.Exec(`INSERT INTO docks (id, organization_id, channel, name, address, status)
		VALUES ($1, $2, 'whatsapp', 'd', '1@s.whatsapp.net', 'linked')`, dock, org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO wa_devices (dock_id, organization_id, jid, registration_id, signed_pre_key_id, secrets)
		VALUES ($1, $2, '1@s.whatsapp.net', 1, 1, '\x00')`, dock, org); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.SecretKey(ctx, cfg, db); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("lost key with sealed devices: %v", err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a new key was generated over sealed devices")
	}
}
