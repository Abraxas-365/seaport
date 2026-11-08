// Package bootstrap is the composition root: it opens the database, runs
// migrations, builds every module and serves HTTP.
package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/puerto/internal/config"
	"github.com/Abraxas-365/puerto/internal/conversation/conversationmodule"
	"github.com/Abraxas-365/puerto/internal/crew/crewmodule"
	"github.com/Abraxas-365/puerto/internal/dock/dockmodule"
	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/event"
	"github.com/Abraxas-365/puerto/internal/event/eventsse"
	"github.com/Abraxas-365/puerto/internal/httpx"
	"github.com/Abraxas-365/puerto/internal/ship"
	"github.com/Abraxas-365/puerto/internal/ship/shipmodule"
	"github.com/Abraxas-365/puerto/migrations"
	"github.com/Abraxas-365/puerto/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
)

// Version is set at build time (-ldflags "-X .../bootstrap.Version=...").
var Version = "dev"

// App is a built port, ready to serve.
type App struct {
	Handler http.Handler
	db      *sqlx.DB
	docks   *dockmodule.Module
	ships   *shipmodule.Module
	cfg     config.Server
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
	// Authenticator overrides the one of cfg.Auth (tests with several
	// organizations).
	Authenticator httpx.Authenticator
}

// OpenDB opens Postgres with session timeouts and UTC.
func OpenDB(ctx context.Context, url string) (*sqlx.DB, error) {
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
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return errx.Wrap(err, "goose dialect", errx.TypeInternal)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
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

	bus := event.NewBus()
	convs := conversationmodule.New(db, bus)
	docks, err := dockmodule.New(ctx, dockmodule.Deps{DB: db, DatabaseURL: cfg.DatabaseURL, WhatsApp: !opts.DisableWhatsApp, Events: bus})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	crew := crewmodule.New(lease, cfg.PublicURL+"/mcp", Version, bus)
	ships := shipmodule.New(shipmodule.Deps{DB: db, Conversations: convs.Service, Docks: docks.Service,
		Messenger: docks.Service, Notifier: crew.Hub, Events: bus,
		Limits: ship.Limits{PerMinute: cfg.SendPerMinute, PerHour: cfg.SendPerHour, ConversationPerMinute: cfg.SendPerChatMinute}})
	docks.BindReceiver(ships.Service)
	crew.Bind(ships.Service, convs.Service)

	done := make(chan struct{})
	var once sync.Once
	auth := opts.Authenticator
	if auth == nil {
		auth = authenticator(cfg)
	}
	session := auth.Session()

	admin := http.NewServeMux()
	docks.Register(admin)
	convs.Register(admin)
	ships.Register(admin)
	crew.Register(admin)
	admin.Handle("GET /api/v1/events", eventsse.Handler(bus, done))
	admin.Handle("DELETE /api/v1/session", session.Logout)
	admin.HandleFunc("/", httpx.NotFound)

	root := http.NewServeMux()
	// Public liveness: nothing about any organization (crew counts are per
	// organization at /api/v1/crew).
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.JSON(w, http.StatusOK, map[string]any{"status": "ok", "version": Version})
	})
	// Public: how the UI must sign in, and session login/state (they check
	// credentials themselves).
	root.Handle("GET /api/v1/config", publicConfig(cfg))
	root.Handle("POST /api/v1/session", session.Login)
	root.Handle("GET /api/v1/session", session.Current)
	root.Handle("/api/", auth.Admin(admin))
	root.Handle("/crew/ws", auth.Crew(crew.WS()))
	root.Handle("/mcp", auth.Crew(crew.MCP()))
	// Everything else is the admin UI (client routes fall back to index.html).
	// One "/" pattern: "GET /" would conflict with the method-less "/api/".
	ui := httpx.SPA(web.Dist())
	root.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpx.NotFound(w, r)
			return
		}
		ui.ServeHTTP(w, r)
	})

	return &App{Handler: httpx.Edges(methodErrors(root)), db: db, docks: docks, ships: ships, cfg: cfg,
		done: done, stopOnce: func() { once.Do(func() { close(done) }) }}, nil
}

// authenticator picks the auth adapter of the run mode; nothing else in
// the port knows the mode.
func authenticator(cfg config.Server) httpx.Authenticator {
	switch cfg.Auth {
	case config.AuthNone:
		return httpx.Open{Organization: config.DefaultOrganization}
	default:
		return httpx.Tokens{Organization: config.DefaultOrganization, CrewToken: cfg.CrewToken,
			Sessions: httpx.Sessions{Token: cfg.AdminToken, TTL: config.SessionTTL,
				Secure: strings.HasPrefix(cfg.PublicURL, "https://")}}
	}
}

// publicConfig is GET /api/v1/config: what the web app needs before
// signing in (the auth mode decides between no login and the token form).
func publicConfig(cfg config.Server) http.Handler {
	body := map[string]any{"auth": cfg.Auth, "version": Version}
	return httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		return httpx.JSON(w, http.StatusOK, body)
	})
}

// Start reconnects linked docks, requeues claims left by a previous process
// and starts the lease sweeper. It returns when ctx ends.
func (a *App) Start(ctx context.Context) {
	if n, err := a.ships.Service.ReleaseExpired(ctx, time.Now().Add(100*365*24*time.Hour)); err != nil {
		slog.Error("requeue claims at start", "err", err)
	} else if n > 0 {
		slog.Info("requeued claims left by a previous run", "ships", n)
	}
	if err := a.docks.Service.RestoreAll(ctx); err != nil {
		slog.Error("restore docks", "err", err)
	}
	go func() {
		t := time.NewTicker(config.SweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := a.ships.Service.ReleaseExpired(ctx, time.Now()); err != nil {
					slog.Error("sweep expired claims", "err", err)
				}
			}
		}
	}()
}

// Close releases the database and gateways.
func (a *App) Close() {
	a.stopOnce()
	a.docks.Close()
	_ = a.db.Close()
}

// Serve runs the HTTP server until ctx ends.
func (a *App) Serve(ctx context.Context) error {
	srv := &http.Server{
		Addr:              a.cfg.Addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("puerto listening", "addr", a.cfg.Addr, "public_url", a.cfg.PublicURL, "version", Version)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errx.Wrap(err, "listen", errx.TypeInternal)
	case <-ctx.Done():
		a.stopOnce() // end SSE streams so Shutdown doesn't wait for them
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
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
		httpx.WriteError(g.ResponseWriter, g.r, errx.MethodNotAllowed("method not allowed"))
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
