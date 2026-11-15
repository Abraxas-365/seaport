package testdb

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The Docker fallback: one throwaway Postgres, shared by every package
// binary of a `go test` run (and by the next runs).
//
// Each package is its own process, so they share through the Docker
// daemon: the container's fixed name is the lock (`docker run --name`
// fails for all but one process; the others wait for it to accept
// connections). It is started with the docker CLI, not testcontainers:
// Ryuk, testcontainers' reaper, removes a container when the process that
// created it exits, which would pull the database from under the packages
// still running. The trade-off is that the container outlives the run
// (cheap, ~30 MB idle, fsync off); `docker rm -f seaport-testdb` removes it.
const (
	containerName = "seaport-testdb"
	image         = "postgres:16-alpine"
	baseDatabase  = "seaport_test"
)

var errNoDocker = errors.New("Docker is not reachable")

func docker(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// dockerServer returns the DSN of the shared container's base database,
// starting the container if needed. errNoDocker when there is no Docker.
func dockerServer() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("%w: no docker command", errNoDocker)
	}
	probe, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if out, err := docker(probe, "version", "--format", "{{.Server.Version}}"); err != nil {
		return "", fmt.Errorf("%w: %s", errNoDocker, out)
	}

	state, _ := docker(ctx, "inspect", "-f", "{{.State.Running}}", containerName)
	switch state {
	case "true":
	case "false":
		if out, err := docker(ctx, "start", containerName); err != nil {
			return "", fmt.Errorf("docker start %s: %s", containerName, out)
		}
	default:
		out, err := docker(ctx, "run", "-d", "--name", containerName,
			"-e", "POSTGRES_USER=seaport", "-e", "POSTGRES_PASSWORD=seaport", "-e", "POSTGRES_DB="+baseDatabase,
			"-p", "127.0.0.1::5432", image,
			// throwaway data: trade durability for speed
			"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off", "-c", "max_connections=300")
		// A name conflict means another process won the race: wait for its container.
		if err != nil && !strings.Contains(out, "is already in use") {
			return "", fmt.Errorf("docker run %s: %s", image, out)
		}
	}

	var dsn string
	for {
		var err error
		if dsn, err = containerDSN(ctx); err == nil && ready(ctx, dsn) == nil {
			return dsn, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("container %s did not accept connections: %v", containerName, err)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// containerDSN builds the DSN from the container's published port.
func containerDSN(ctx context.Context) (string, error) {
	out, err := docker(ctx, "port", containerName, "5432/tcp")
	if err != nil {
		return "", errors.New(out)
	}
	// "127.0.0.1:49153", possibly followed by an IPv6 line
	line, _, _ := strings.Cut(out, "\n")
	if line == "" {
		return "", errors.New("no published port yet")
	}
	return "postgres://seaport:seaport@" + strings.TrimSpace(line) + "/" + baseDatabase + "?sslmode=disable", nil
}

func ready(ctx context.Context, dsn string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	return conn.Close(ctx)
}
