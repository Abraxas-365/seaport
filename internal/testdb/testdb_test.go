package testdb

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPackageSuffix(t *testing.T) {
	for pkg, want := range map[string]string{
		"internal/crew/adapters/crewpg": "crewpg",
		"internal/pglock":               "pglock",
		"internal/bootstrap":            "bootstrap",
		"cmd/seaport":                   "cmd_seaport",
	} {
		if got := packageSuffix(pkg); got != want {
			t.Errorf("packageSuffix(%q) = %q, want %q", pkg, got, want)
		}
	}
}

func TestDatabaseName(t *testing.T) {
	got, err := databaseName("Seaport-Test", "internal/dock/adapters/dockpg")
	if err != nil || got != "seaport_test_dockpg" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := databaseName(strings.Repeat("a", 60), "internal/dock/adapters/dockpg"); err == nil {
		t.Fatal("a name over 63 bytes must be refused")
	}
}

// The package of this test file is internal/testdb, whatever the build flags.
func TestCallerPackage(t *testing.T) {
	if got := callerPackage(t, 1); got != "internal/testdb" {
		t.Fatalf("callerPackage = %q", got)
	}
}

// URL gives this package its own database next to the server's base one,
// and a database made for another package under the same name is refused.
func TestURLCreatesThePackageDatabase(t *testing.T) {
	dsn := URL(t)
	if !strings.Contains(dsn, "_testdb") {
		t.Fatalf("dsn %q should name the testdb database", dsn)
	}
	if URL(t) != dsn {
		t.Fatal("URL is cached per package")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var name string
	if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(name, "_testdb")
	baseDSN := strings.Replace(dsn, name, base, 1)
	if _, err := ensure(baseDSN, "internal/other/testdb"); err == nil || !strings.Contains(err.Error(), "belongs to package") {
		t.Fatalf("a name collision must be refused, got %v", err)
	}
}
