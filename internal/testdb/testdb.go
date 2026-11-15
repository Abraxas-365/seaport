// Package testdb hands integration tests a Postgres database of their own.
//
// Every package that touches Postgres wipes its database per test, so two
// packages sharing one would corrupt each other and `go test` needed `-p 1`.
// URL gives each test package a database dedicated to it
// (`<base>_<package>`) on a shared server, so packages run in parallel.
//
// Where the server comes from, in order:
//
//  1. SEAPORT_TEST_DATABASE_URL: any Postgres (docker compose, a CI
//     service). Its database is the "base": the package databases are
//     created next to it, on the same server, and the role needs CREATEDB.
//  2. Docker: one throwaway postgres container named "seaport-testdb"
//     shared by every package of the run (see docker.go).
//  3. Neither: the test skips, saying how to get one (fails in CI).
//
// testdb only hands out a DSN: callers open it (bootstrap.OpenDB), wipe and
// migrate it as before, since foundation packages can't import bootstrap.
// Only _test.go files may import it (TestOnlyTestsImportTestdb).
package testdb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Env names the variable that points the tests at a Postgres server.
const Env = "SEAPORT_TEST_DATABASE_URL"

// modulePath is the module's import path (what -trimpath leaves of a source path).
const modulePath = "github.com/Abraxas-365/seaport"

// advisoryKey serializes CREATE DATABASE across processes: Postgres fails
// concurrent creations from the same template ("being accessed by other users").
const advisoryKey = 7_401_900

var (
	mu    sync.Mutex
	cache = map[string]string{} // package → DSN
)

// URL is the DSN of the database dedicated to the calling test's package,
// created if missing. It skips the test (fails in CI) when there is no
// server to ask. The result is cached per process.
func URL(t testing.TB) string {
	t.Helper()
	pkg := callerPackage(t, 2)

	mu.Lock()
	defer mu.Unlock()
	if dsn, ok := cache[pkg]; ok {
		return dsn
	}
	base := os.Getenv(Env)
	if base == "" {
		var err error
		switch base, err = dockerServer(); {
		case errors.Is(err, errNoDocker):
			unavailable(t, err)
		case err != nil:
			t.Fatalf("testdb: starting the test Postgres in Docker: %v", err)
		}
	}
	dsn, err := ensure(base, pkg)
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	cache[pkg] = dsn
	return dsn
}

// unavailable skips, or fails in CI, where a missing database must not pass quietly.
func unavailable(t testing.TB, cause error) {
	t.Helper()
	msg := "no test database: set " + Env + " to a Postgres server (docker compose up -d, " +
		"createdb seaport_test; then " + Env + "=postgres://seaport:seaport@127.0.0.1:5449/seaport_test?sslmode=disable) " +
		"or start Docker and leave it unset (" + cause.Error() + ")"
	if os.Getenv("CI") != "" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

// callerPackage is the directory of the test file skip frames up, relative
// to the module root (`internal/crew/adapters/crewpg`, `cmd/seaport`). The
// source path is used rather than the function name because package main
// is just "main" there. With -trimpath the path is module-relative already;
// otherwise the module root is the nearest directory with a go.mod.
func callerPackage(t testing.TB, skip int) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(skip)
	if !ok {
		t.Fatal("testdb: cannot find the calling package")
	}
	dir := filepath.ToSlash(filepath.Dir(file))
	if rel, ok := strings.CutPrefix(dir, modulePath+"/"); ok {
		return rel
	}
	for root := filepath.Dir(file); ; root = filepath.Dir(root) {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			rel, err := filepath.Rel(root, filepath.Dir(file))
			if err != nil {
				t.Fatalf("testdb: %v", err)
			}
			return filepath.ToSlash(rel)
		}
		if root == filepath.Dir(root) {
			t.Fatalf("testdb: no go.mod above %s", file)
		}
	}
}

var unsafeName = regexp.MustCompile(`[^a-z0-9_]+`)

// packageSuffix names a package inside database names: internal packages by
// their last path element (`internal/crew/adapters/crewpg` → `crewpg`),
// others by the whole path (`cmd/seaport` → `cmd_seaport`). Two packages
// that share a last element are caught by ensure (the database records its
// package).
func packageSuffix(pkg string) string {
	rel := pkg
	if after, ok := strings.CutPrefix(rel, "internal/"); ok {
		rel = after[strings.LastIndex(after, "/")+1:]
	}
	return unsafeName.ReplaceAllString(strings.ToLower(rel), "_")
}

// databaseName is `<base>_<package>`: a valid identifier of at most 63 bytes.
func databaseName(base, pkg string) (string, error) {
	name := unsafeName.ReplaceAllString(strings.ToLower(base), "_") + "_" + packageSuffix(pkg)
	if len(name) > 63 {
		return "", fmt.Errorf("database name %q is longer than 63 bytes; use a shorter base database", name)
	}
	return name, nil
}

// ensure creates the package's database next to the base one (if missing)
// and returns its DSN.
func ensure(baseDSN, pkg string) (string, error) {
	u, err := url.Parse(baseDSN)
	if err != nil || u.Scheme == "" || strings.Trim(u.Path, "/") == "" {
		return "", fmt.Errorf("%s must be a postgres:// URL naming a database", Env)
	}
	name, err := databaseName(strings.Trim(u.Path, "/"), pkg)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, baseDSN)
	if err != nil {
		return "", fmt.Errorf("connect to the test server (%s): %w", Env, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryKey); err != nil {
		return "", err
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryKey) }()

	quoted := pgx.Identifier{name}.Sanitize()
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		if _, err := conn.Exec(ctx, `CREATE DATABASE `+quoted); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42501" {
				return "", fmt.Errorf("creating %s: the role needs CREATEDB (ALTER ROLE ... CREATEDB): %w", name, err)
			}
			return "", fmt.Errorf("creating %s: %w", name, err)
		}
	}
	// The database's comment records the package it was made for, so two
	// packages that map to one name fail loudly instead of sharing a
	// database. One no one marked (made by hand) is adopted.
	var owner *string
	if err := conn.QueryRow(ctx, `SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = $1`, name).Scan(&owner); err != nil {
		return "", err
	}
	switch {
	case owner == nil || *owner == "":
		if _, err := conn.Exec(ctx, `COMMENT ON DATABASE `+quoted+` IS `+quoteLiteral(pkg)); err != nil {
			return "", err
		}
	case *owner != pkg:
		return "", fmt.Errorf("database %s belongs to package %s, and %s maps to the same name: rename one package or the base database", name, *owner, pkg)
	}
	u.Path = "/" + name
	return u.String(), nil
}

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
