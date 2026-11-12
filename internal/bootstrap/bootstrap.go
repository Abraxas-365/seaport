// Package bootstrap is the composition root: it opens the database, runs
// migrations, builds every module and serves HTTP.
package bootstrap

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent/agentmodule"
	"github.com/Abraxas-365/seaport/internal/auth/authmodule"
	"github.com/Abraxas-365/seaport/internal/blob/adapters/gocloudblob"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation/conversationmodule"
	"github.com/Abraxas-365/seaport/internal/crew/crewmodule"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockgithub"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockjira"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockslack"
	"github.com/Abraxas-365/seaport/internal/dock/dockmodule"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/event/eventpg"
	"github.com/Abraxas-365/seaport/internal/event/eventsse"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/hosting/hostingmodule"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/httpx/stdhttp"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/ship/shipmodule"
	"github.com/Abraxas-365/seaport/migrations"
	"github.com/Abraxas-365/seaport/web"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
)

// Version is set at build time (-ldflags "-X .../bootstrap.Version=...").
var Version = "dev"

// App is a built port, ready to serve.
type App struct {
	// web is the HTTP server: the Fiber shell with the net/http routes
	// behind it (server.go).
	web    *fiber.App
	db     *sqlx.DB
	lockDB *sqlx.DB
	bus    *event.Bus
	crew   *crewmodule.Module
	docks  *dockmodule.Module
	ships  *shipmodule.Module
	hosted *hostingmodule.Module
	// signIn is iam mode's sign-in (nil in the other modes).
	signIn *authmodule.Module
	// blobs keeps message media (nil: off).
	blobs *gocloudblob.Store
	sweep time.Duration
	cfg   config.Server
	// done ends long-lived streams (SSE) on shutdown.
	done     chan struct{}
	stopOnce func()
}

// Options tune Build for tests.
type Options struct {
	// DisableWhatsApp skips the WhatsApp gateway.
	DisableWhatsApp bool
	// Lease overrides config.ClaimLease.
	Lease time.Duration
	// DockLease overrides config.DockLease.
	DockLease time.Duration
	// Gateways are extra dock gateways (tests: a fake exclusive channel).
	Gateways []dock.Gateway
	// Runtime runs hosted agents instead of cfg.Runtime's (tests).
	Runtime hosting.Runtime
	// SupervisePeriod overrides hosting.SupervisePeriod (tests).
	SupervisePeriod time.Duration
	// SweepInterval overrides config.SweepInterval (tests).
	SweepInterval time.Duration
	// EgressLookup resolves the egress proxy's hosts (tests).
	EgressLookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Authenticator overrides the one of cfg.Auth (tests with several
	// organizations).
	Authenticator httpx.Authenticator
}

// OpenDB opens Postgres with session timeouts and UTC.
func OpenDB(ctx context.Context, url string) (*sqlx.DB, error) {
	return openDB(ctx, url, nil)
}

// openLockDB opens the small pool of session advisory locks (pglock):
// no statement_timeout, since waiting for a lock is a statement (callers
// bound it with their context).
func openLockDB(ctx context.Context, url string) (*sqlx.DB, error) {
	db, err := openDB(ctx, url, map[string]string{"statement_timeout": "0"})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	return db, nil
}

// openDB applies the default session parameters (the URL may set its
// own) and then force (always).
func openDB(ctx context.Context, url string, force map[string]string) (*sqlx.DB, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, errx.Wrap(err, "parse DATABASE_URL", errx.TypeValidation)
	}
	for k, v := range map[string]string{"timezone": "UTC", "statement_timeout": "30000",
		"idle_in_transaction_session_timeout": "60000"} {
		if _, ok := cfg.RuntimeParams[k]; !ok {
			cfg.RuntimeParams[k] = v
		}
	}
	for k, v := range force {
		cfg.RuntimeParams[k] = v
	}
	db := sqlx.NewDb(stdlib.OpenDB(*cfg), "pgx")
	db.SetMaxOpenConns(20)
	db.SetConnMaxIdleTime(5 * time.Minute)
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		_ = db.Close()
		return nil, errx.Wrap(err, "connect to Postgres", errx.TypeExternal)
	}
	return db, nil
}

// Migrate applies the embedded migrations.
func Migrate(ctx context.Context, db *sql.DB) error {
	return MigrateTo(ctx, db, 0)
}

// MigrateTo applies the embedded migrations up to version (0: all); tests
// use it to check a migration against the data before it.
func MigrateTo(ctx context.Context, db *sql.DB, version int64) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return errx.Wrap(err, "goose dialect", errx.TypeInternal)
	}
	var err error
	if version == 0 {
		err = goose.UpContext(ctx, db, ".")
	} else {
		err = goose.UpToContext(ctx, db, ".", version)
	}
	if err != nil {
		return errx.Wrap(err, "run migrations", errx.TypeInternal)
	}
	return nil
}

// Build wires the whole port.
func Build(ctx context.Context, cfg config.Server, opts Options) (*App, error) {
	db, err := OpenDB(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, db.DB); err != nil {
		_ = db.Close()
		return nil, err
	}
	lease := opts.Lease
	if lease == 0 {
		lease = config.ClaimLease
	}
	sweep := opts.SweepInterval
	if sweep <= 0 {
		sweep = config.SweepInterval
	}
	dockLease := opts.DockLease
	if dockLease == 0 {
		dockLease = config.DockLease
	}
	// This process among those serving the database (crew connections,
	// dock ownership).
	process := newProcessID()

	// Session advisory locks (per-dock customs) hold a connection while the
	// work runs on db: a pool of their own can't starve it.
	lockDB, err := openLockDB(ctx, cfg.DatabaseURL)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	closeDBs := func() { _ = lockDB.Close(); _ = db.Close() }
	// Started here, so a port that can't read its event log doesn't start.
	bus := event.NewBus(eventpg.New(db))
	if err := bus.Start(ctx); err != nil {
		closeDBs()
		return nil, err
	}
	convs := conversationmodule.New(db, lockDB.DB, bus)
	// The secret key seals the WhatsApp store and agent secrets. Without
	// WhatsApp (tests) it is loaded only when given; agent secrets are
	// then refused.
	var secretKey string
	if !opts.DisableWhatsApp || cfg.SecretKey != "" {
		if secretKey, err = SecretKey(ctx, cfg, db); err != nil {
			bus.Close()
			closeDBs()
			return nil, err
		}
	}
	docks, err := dockmodule.New(ctx, dockmodule.Deps{DB: db, SecretKey: secretKey, WhatsApp: !opts.DisableWhatsApp, Events: bus,
		MaxDocks: cfg.MaxDocks, Process: process, DockLease: dockLease, Gateways: opts.Gateways,
		GitHub: dockgithub.Config{PublicURL: cfg.PublicURL, WebURL: cfg.GitHubURL, APIURL: cfg.GitHubAPIURL, OIDCURL: cfg.GitHubOIDCURL},
		Jira:   dockjira.Config{PublicURL: cfg.PublicURL, Hosts: cfg.JiraHosts},
		Slack:  dockslack.Config{PublicURL: cfg.PublicURL, WebURL: cfg.SlackURL, APIURL: cfg.SlackAPIURL}})
	if err != nil {
		bus.Close()
		closeDBs()
		return nil, err
	}
	crew := crewmodule.New(db, process, lease, cfg.PublicURL+"/mcp", Version, bus, cfg.MaxCrewConnections)
	agents, err := agentmodule.New(db, secretKey, crew.Hub, bus)
	if err != nil {
		docks.Close()
		bus.Close()
		closeDBs()
		return nil, err
	}
	// New work wakes the agent's connections and, for a hosted agent with
	// no slot up, the slot supervisor (bound once hosting is built).
	notify := &fanout{first: crew.Hub}
	// Message media: sealed with the secret key, so none without one.
	var blobs *gocloudblob.Store
	if cfg.BlobURL != "" && secretKey != "" {
		if blobs, err = gocloudblob.Open(ctx, cfg.BlobURL); err != nil {
			docks.Close()
			bus.Close()
			closeDBs()
			return nil, err
		}
	}
	shipDeps := shipmodule.Deps{DB: db, Conversations: convs.Service, Docks: docks.Service,
		Messenger: docks.Service, Notifier: notify, Events: bus, Agents: agents.Service,
		Limits: ship.Limits{PerMinute: cfg.SendPerMinute, PerHour: cfg.SendPerHour, ConversationPerMinute: cfg.SendPerChatMinute,
			OrganizationPerHour: cfg.SendOrgPerHour}, SecretKey: secretKey}
	if blobs != nil {
		shipDeps.Blobs = blobs
	} else {
		slog.Warn("message media are off (no SEAPORT_BLOB_URL or no secret key): media arrive as notes")
	}
	ships, err := shipmodule.New(shipDeps)
	if err != nil {
		closeBlobs(blobs)
		docks.Close()
		bus.Close()
		closeDBs()
		return nil, err
	}
	docks.BindReceiver(ships.Service)
	docks.BindTeams(agents.Service)
	convs.Service.Bind(ships.Service)
	crew.Bind(ships.Service, convs.Service, agents.Service)
	hostCfg := hostingmodule.Config{
		Limits: hosting.Limits{MaxCPUs: float64(cfg.HostMaxCPUs), MaxMemoryMB: cfg.HostMaxMemoryMB, MaxSlots: cfg.MaxSlots},
		Docker: cfg.Runtime == config.RuntimeDocker, Runtime: opts.Runtime, Bases: baseImages(cfg),
		Network: cfg.RuntimeNetwork, ExtraHosts: hostGateway(cfg), SupervisePeriod: opts.SupervisePeriod,
		GatewayAnthropic: cfg.GatewayAnthropicURL, SecretKey: secretKey}
	if blobs != nil {
		hostCfg.Blobs = blobs
	}
	if cfg.Auth != config.AuthNone { // slots sign in with tokens: none in open mode
		hostCfg.Credentials, hostCfg.Kits, hostCfg.AgentURL = crew.Tokens, agents.Service, cfg.AgentURL
		if hostCfg.AgentURL == "" {
			hostCfg.AgentURL = cfg.PublicURL
		}
		hostCfg.EgressAddr, hostCfg.EgressURL, hostCfg.Tokens = cfg.EgressAddr, cfg.EgressURL, crew.Resolver
		hostCfg.EgressPrivate, hostCfg.EgressLookup = cfg.EgressPrivate, opts.EgressLookup
	}
	hosted, err := hostingmodule.New(ctx, db, agents.Service, hostCfg, bus)
	if err != nil {
		closeBlobs(blobs)
		docks.Close()
		bus.Close()
		closeDBs()
		return nil, err
	}
	notify.bind(hosted)

	done := make(chan struct{})
	var once sync.Once
	auth := opts.Authenticator
	var signIn *authmodule.Module
	if auth == nil {
		if cfg.Auth == config.AuthIAM {
			if signIn, err = authmodule.New(db, cfg, crew.Resolver); err != nil {
				closeBlobs(blobs)
				hosted.Close()
				docks.Close()
				bus.Close()
				closeDBs()
				return nil, err
			}
			auth = signIn.Authenticator
		} else {
			auth = authenticator(cfg, crew.Resolver)
		}
	}

	// The net/http routes of the domains not migrated to Fiber yet, behind
	// the Fiber app (server.go): admin ones (already past the Fiber
	// middleware of /api/) and the rest.
	admin := http.NewServeMux()
	crew.Register(admin)
	agents.Register(admin)
	hosted.Register(admin)
	admin.Handle("GET /api/v1/events", eventsse.Handler(bus, done, EventPermissions))
	admin.HandleFunc("/", stdhttp.NotFound)

	root := http.NewServeMux()
	root.Handle("/api/", admin)
	root.Handle("/crew/ws", crew.WS())
	root.Handle("/mcp", crew.MCP())
	root.Handle("GET /crew/profile", crew.Kit())
	root.Handle("GET /crew/attachments/{id}", crew.Attachment())
	root.Handle("POST /crew/reply", crew.Reply())
	root.Handle("GET /crew/workspace", hosted.Workspace())
	root.Handle(hosting.GatewayPath+"/", hosted.Gateway())
	// Everything else is the admin UI (client routes fall back to index.html).
	ui := stdhttp.SPA(web.Dist(), cfg.GitHubURL)
	root.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			stdhttp.NotFound(w, r)
			return
		}
		ui.ServeHTTP(w, r)
	})

	srv := newServer(methodErrors(root), func(app *fiber.App, legacy fiber.Handler) {
		mountNative(app, legacy, nativeRoutes{cfg: cfg, auth: auth, limit: httpx.NewRateLimit(cfg.APIRate, cfg.APIBurst),
			admin:  []func(httpx.Router){convs.Register, ships.Register, docks.Register},
			public: []func(httpx.Router){docks.RegisterPublic}, githubToken: docks.GitHubToken()})
	})

	return &App{web: srv, db: db, lockDB: lockDB, bus: bus, crew: crew, docks: docks, ships: ships, hosted: hosted, signIn: signIn, cfg: cfg,
		blobs: blobs, sweep: sweep, done: done, stopOnce: func() { once.Do(func() { close(done) }) }}, nil
}

// newProcessID names this process among those serving one database:
// readable in logs (host, pid) and unique across restarts (random part).
func newProcessID() string {
	host, _ := os.Hostname()
	if len(host) > 40 {
		host = host[:40]
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(b[:]))
}

// authenticator picks the auth adapter of the run mode; nothing else in
// the port knows the mode.
func authenticator(cfg config.Server, crewTokens httpx.CrewResolver) httpx.Authenticator {
	switch cfg.Auth {
	case config.AuthNone:
		return httpx.Open{Organization: config.DefaultOrganization}
	default:
		return httpx.Tokens{Organization: config.DefaultOrganization, CrewTokens: crewTokens,
			Sessions: httpx.Sessions{Token: cfg.AdminToken, TTL: config.SessionTTL,
				Secure: strings.HasPrefix(cfg.PublicURL, "https://")}}
	}
}

// hostGateway maps host.docker.internal to the host (Linux engines don't
// by themselves), so a slot reaches a port on the same machine.
func hostGateway(cfg config.Server) []string {
	if !cfg.RuntimeHostGateway {
		return nil
	}
	return []string{"host.docker.internal:host-gateway"}
}

// fanout notifies the crew hub, then (once bound) the slot supervisor.
type fanout struct {
	first ship.Notifier
	mu    sync.RWMutex
	then  ship.Notifier
}

func (f *fanout) bind(n ship.Notifier) {
	f.mu.Lock()
	f.then = n
	f.mu.Unlock()
}

func (f *fanout) Notify(org identity.OrganizationID, agent identity.AgentID) {
	f.first.Notify(org, agent)
	f.mu.RLock()
	then := f.then
	f.mu.RUnlock()
	if then != nil {
		then.Notify(org, agent)
	}
}

// baseImages are the harnesses' base images: SEAPORT_IMAGE_*, else the
// ones published with this version.
func baseImages(cfg config.Server) map[string]string {
	out := map[string]string{
		hosting.HarnessRness:      cfg.ImageRness,
		hosting.HarnessClaudeCode: cfg.ImageClaudeCode,
	}
	for harness, image := range out {
		if image == "" {
			out[harness] = config.BaseImage(harness, Version)
		}
	}
	return out
}

// Start starts the crew hub (signals, presence, reaping a stopped
// process's connections), dock ownership (this process takes and connects
// the docks no live process runs) and the sweepers. It returns at once;
// they run until ctx ends or Close. Claims of other processes are left
// alone: a stopped process's are released by the reaper (or their lease).
func (a *App) Start(ctx context.Context) {
	a.crew.Hub.Start(ctx)
	a.docks.Start(ctx)
	a.hosted.Start(ctx)
	if a.signIn != nil {
		go a.signIn.Sweep(ctx)
	}
	go func() {
		t := time.NewTicker(a.sweep)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := a.ships.Service.ReleaseExpired(ctx, time.Now()); err != nil {
					slog.Error("sweep expired claims", "err", err)
				}
				if _, err := a.bus.Sweep(ctx, time.Now().Add(-config.EventRetention)); err != nil {
					slog.Error("sweep old events", "err", err)
				}
				// homes first: their blobs go to the trash, due now
				if _, err := a.hosted.Service.SweepHomes(ctx, time.Now()); err != nil {
					slog.Error("sweep unused homes", "err", err)
				}
				if _, err := a.ships.Service.SweepTrash(ctx, time.Now()); err != nil {
					slog.Error("sweep unused blobs", "err", err)
				}
			}
		}
	}()
}

// Close releases the database and gateways.
func (a *App) Close() {
	a.stopOnce()
	// First: connections release their deliveries and docks are handed
	// over (other processes take them at once), which needs the database
	// and the bus.
	a.crew.Hub.Close()
	a.docks.Close()
	a.hosted.Close() // builds record their end (database and bus still open)
	a.bus.Close()
	closeBlobs(a.blobs)
	_ = a.lockDB.Close()
	_ = a.db.Close()
}

func closeBlobs(b *gocloudblob.Store) {
	if b != nil {
		_ = b.Close()
	}
}

// methodErrors turns the mux's plain-text 405 into the standard error body.
func methodErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&statusGuard{ResponseWriter: w, r: r}, r)
	})
}

type statusGuard struct {
	http.ResponseWriter
	r          *http.Request
	suppressed bool
}

func (g *statusGuard) WriteHeader(status int) {
	if status == http.StatusMethodNotAllowed && g.Header().Get("Content-Type") == "text/plain; charset=utf-8" {
		g.suppressed = true
		g.Header().Del("Content-Type")
		g.Header().Del("X-Content-Type-Options")
		stdhttp.WriteError(g.ResponseWriter, g.r, errx.MethodNotAllowed("method not allowed"))
		return
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *statusGuard) Write(b []byte) (int, error) {
	if g.suppressed {
		return len(b), nil
	}
	return g.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController (and the WebSocket upgrade) reach
// the underlying writer.
func (g *statusGuard) Unwrap() http.ResponseWriter { return g.ResponseWriter }
