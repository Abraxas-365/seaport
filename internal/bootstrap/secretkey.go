package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/jmoiron/sqlx"
)

// SecretKey is SEAPORT_SECRET_KEY, or else the key in cfg.SecretKeyFile,
// generated there (0600) on first start. It seals the WhatsApp store,
// agent secrets, message media, harness homes, GitHub Apps and Jira sites, so a new key is never generated while sealed values exist:
// that would lose them (every dock linked again, every secret set again).
func SecretKey(ctx context.Context, cfg config.Server, db *sqlx.DB) (string, error) {
	if cfg.SecretKey != "" {
		return cfg.SecretKey, config.CheckSecretKey(cfg.SecretKey)
	}
	path := cfg.SecretKeyFile
	if path == "" {
		return "", errx.Validation("set SEAPORT_SECRET_KEY or SEAPORT_SECRET_KEY_FILE (no user config directory to keep a generated key in)")
	}
	key, err := readKeyFile(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}
	var sealed bool
	if err := db.GetContext(ctx, &sealed, `SELECT EXISTS (SELECT 1 FROM wa_devices) OR EXISTS (SELECT 1 FROM agent_secrets) OR EXISTS (SELECT 1 FROM ship_attachments) OR EXISTS (SELECT 1 FROM hosting_homes) OR EXISTS (SELECT 1 FROM github_apps) OR EXISTS (SELECT 1 FROM jira_sites)`); err != nil {
		return "", errx.Wrap(err, "check sealed values", errx.TypeInternal)
	}
	if sealed {
		return "", errx.Validation("the WhatsApp store, the agent secrets, message media, harness homes, GitHub Apps or Jira sites hold values sealed with a key that is not here: restore " + path +
			" (or set SEAPORT_SECRET_KEY to that key); a new key can't open them")
	}
	return generateKeyFile(path)
}

func readKeyFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", errx.Wrap(err, "read secret key file", errx.TypeInternal)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errx.Validation(path + " is readable by other users; chmod 600 it")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errx.Wrap(err, "read secret key file", errx.TypeInternal)
	}
	key := strings.TrimSpace(string(raw))
	if err := config.CheckSecretKey(key); err != nil {
		return "", errx.Validation(path + ": " + err.Error())
	}
	return key, nil
}

// generateKeyFile writes the key complete and synced to a temporary file,
// then links it into place: readers never see a partial key, and a second
// process racing to generate reads the winner's.
func generateKeyFile(path string) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", errx.Wrap(err, "create secret key directory", errx.TypeInternal)
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw) // never fails (crypto/rand)
	key := base64.RawURLEncoding.EncodeToString(raw)
	f, err := os.CreateTemp(dir, ".secret-key-*") // 0600
	if err != nil {
		return "", errx.Wrap(err, "create secret key file", errx.TypeInternal)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.WriteString(key + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", errx.Wrap(err, "write secret key file", errx.TypeInternal)
	}
	if err := os.Link(f.Name(), path); errors.Is(err, fs.ErrExist) {
		return readKeyFile(path) // another process won the race
	} else if err != nil {
		return "", errx.Wrap(err, "create secret key file", errx.TypeInternal)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // the new name survives a crash
		_ = d.Close()
	}
	slog.Warn("generated the secret key that seals WhatsApp keys and agent secrets; back it up apart from the database (a dump without it is useless, a lost key means linking every dock and setting every agent secret again)", "file", path)
	return key, nil
}
