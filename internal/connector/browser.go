package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// The managed browser of a hosted slot (plan browser-login.md): the
// environment's engine (Chromium) runs beside the harness with the
// agent's profile in the state volume, speaks CDP on loopback
// (hosting.BrowserPort), and the agent reaches it through the browser MCP
// (chrome-devtools-mcp, stdio) the connector adds to every session. The
// port's viewer reaches the same CDP through `seaport browser-relay`
// (Runtime.Exec), so a person signs in to a site and the agent has the
// session.

// BrowserMCP is the browser MCP server the image installs when the
// environment has a browser (hosting.Dockerfile), and the name the agent
// sees it under.
const (
	BrowserMCP     = "/opt/seaport/browser/bin/chrome-devtools-mcp"
	BrowserMCPName = "browser"
)

// browserReady is how long the engine gets to answer /json/version.
const browserReady = 30 * time.Second

// Browser is a running engine: its process and the proxy bridge it goes
// out through.
type Browser struct {
	Engine string
	// URL is the CDP base (http://127.0.0.1:9222).
	URL string
	cmd *exec.Cmd
	// bridge is the loopback proxy that adds the slot's credentials to
	// each request (Chromium takes no credentials in --proxy-server).
	bridge *http.Server
	done   chan struct{}
}

// browserURL is the CDP base of a slot's browser.
func browserURL() string { return "http://127.0.0.1:" + strconv.Itoa(hosting.BrowserPort) }

// StartBrowser runs engine with its profile under state (hosting.BrowserDir)
// and waits until CDP answers. Every site goes through the slot's egress
// proxy (HTTPS_PROXY) by a loopback bridge that adds the proxy's basic
// auth; NO_PROXY hosts bypass it. It returns nil, nil when engine is "".
func StartBrowser(ctx context.Context, engine, state string) (*Browser, error) {
	switch engine {
	case "":
		return nil, nil
	case hosting.BrowserChromium:
	default:
		return nil, errx.Validation("unknown browser engine " + engine)
	}
	profile := filepath.Join(state, hosting.BrowserDir)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, errx.Wrap(err, "prepare the browser profile", errx.TypeInternal)
	}
	b := &Browser{Engine: engine, URL: browserURL(), done: make(chan struct{})}
	args := []string{
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--disable-crash-reporter", "--no-crashpad", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--disable-sync", "--disable-features=Translate",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=" + strconv.Itoa(hosting.BrowserPort),
		"--remote-allow-origins=*", "--user-data-dir=" + profile, "--window-size=1280,800",
		"--lang=en-US", // sites render in a predictable language for the agent
	}
	if proxy := os.Getenv("HTTPS_PROXY"); proxy != "" {
		addr, err := b.startBridge(proxy)
		if err != nil {
			return nil, err
		}
		args = append(args, "--proxy-server=http://"+addr)
		if bypass := proxyBypass(os.Getenv("NO_PROXY")); bypass != "" {
			args = append(args, "--proxy-bypass-list="+bypass)
		}
	}
	args = append(args, "about:blank")
	cmd := exec.Command("chromium", args...)
	cmd.Env = append(os.Environ(), "HOME="+os.Getenv("HOME"))
	cmd.Stdout = io.Discard
	cmd.Stderr = &browserLog{} // dbus noise is dropped; the rest is logged
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		b.closeBridge()
		return nil, errx.Wrap(err, "start the browser", errx.TypeInternal)
	}
	b.cmd = cmd
	go func() {
		_ = cmd.Wait()
		close(b.done)
	}()
	if err := b.waitReady(ctx); err != nil {
		b.Stop()
		return nil, err
	}
	slog.Info("browser started", "engine", engine, "profile", profile)
	return b, nil
}

// Done closes when the engine exits.
func (b *Browser) Done() <-chan struct{} { return b.done }

// Stop ends the engine (and every renderer of its process group) and the
// bridge.
func (b *Browser) Stop() {
	if b == nil {
		return
	}
	if b.cmd != nil && b.cmd.Process != nil {
		_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
			<-b.done
		}
	}
	b.closeBridge()
}

// MCPServer is the ACP stdio entry of the browser MCP for the harness
// (nil when the image has no browser MCP).
func (b *Browser) MCPServer() (name, command string, args []string, ok bool) {
	if b == nil {
		return "", "", nil, false
	}
	if _, err := os.Stat(BrowserMCP); err != nil {
		slog.Warn("the image has no browser MCP; the agent gets no browser tools", "path", BrowserMCP)
		return "", "", nil, false
	}
	// No telemetry from a slot: no usage statistics, no CrUX lookups.
	return BrowserMCPName, BrowserMCP, []string{"--browserUrl", b.URL, "--no-usage-statistics", "--no-performance-crux"}, true
}

// waitReady polls /json/version.
func (b *Browser) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(browserReady)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-b.done:
			return errx.Internal("the browser exited at start")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(b.URL + "/json/version")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errx.Internal("the browser did not answer on " + b.URL + " within " + browserReady.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// startBridge serves a plain forward proxy on loopback that sends every
// request (CONNECT included) to upstream with its credentials.
func (b *Browser) startBridge(upstream string) (string, error) {
	u, err := url.Parse(upstream)
	if err != nil || u.Host == "" {
		return "", errx.Validation("HTTPS_PROXY is not a URL")
	}
	auth := ""
	if u.User != nil {
		auth = "Basic " + basicAuth(u.User.String())
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", errx.Wrap(err, "start the browser's proxy bridge", errx.TypeInternal)
	}
	br := &bridge{upstream: u.Host, auth: auth}
	b.bridge = &http.Server{Handler: br, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = b.bridge.Serve(ln) }()
	return ln.Addr().String(), nil
}

func (b *Browser) closeBridge() {
	if b.bridge != nil {
		_ = b.bridge.Close()
	}
}

// bridge forwards each request to the upstream proxy with the slot's
// Proxy-Authorization: CONNECT by splicing the connections, plain http by
// forwarding the request as a proxy request.
type bridge struct {
	upstream string
	auth     string
}

func (p *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	// plain http: forward to the upstream proxy as is (absolute URL)
	out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out.Header = r.Header.Clone()
	if p.auth != "" {
		out.Header.Set("Proxy-Authorization", p.auth)
	}
	tr := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: p.upstream}), DisableCompression: true}
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "upstream proxy unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *bridge) connect(w http.ResponseWriter, r *http.Request) {
	up, err := net.DialTimeout("tcp", p.upstream, 10*time.Second)
	if err != nil {
		http.Error(w, "upstream proxy unreachable", http.StatusBadGateway)
		return
	}
	req := "CONNECT " + r.Host + " HTTP/1.1\r\nHost: " + r.Host + "\r\n"
	if p.auth != "" {
		req += "Proxy-Authorization: " + p.auth + "\r\n"
	}
	req += "\r\n"
	if _, err := io.WriteString(up, req); err != nil {
		up.Close()
		http.Error(w, "upstream proxy unreachable", http.StatusBadGateway)
		return
	}
	upr := bufio.NewReader(up)
	resp, err := http.ReadResponse(upr, r)
	if err != nil {
		up.Close()
		http.Error(w, "upstream proxy unreachable", http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusOK {
		up.Close()
		// the egress proxy's refusal (host not allowed) reaches the browser
		// as it is, so the agent sees why
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return
	}
	down, downBuf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = io.WriteString(down, "HTTP/1.1 200 Connection Established\r\n\r\n")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(up, downBuf)
		closeWrite(up)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(down, upr)
		closeWrite(down)
	}()
	wg.Wait()
	up.Close()
	down.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// proxyBypass turns NO_PROXY (a,b,c) into Chromium's bypass list
// (a;b;c; <-loopback> for the loopback names).
func proxyBypass(noProxy string) string {
	var out []string
	for _, h := range strings.Split(noProxy, ",") {
		h = strings.TrimSpace(h)
		switch h {
		case "":
		case "localhost", "127.0.0.1", "::1":
			if len(out) == 0 || out[0] != "<-loopback>" {
				out = append([]string{"<-loopback>"}, out...)
			}
		default:
			out = append(out, h)
		}
	}
	return strings.Join(out, ";")
}

// browserLog logs the engine's stderr lines but the known noise (dbus and
// the like in a container).
type browserLog struct {
	mu  sync.Mutex
	buf []byte
}

func (l *browserLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := strings.IndexByte(string(l.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(l.buf[:i]))
		l.buf = l.buf[i+1:]
		if line == "" || strings.Contains(line, "dbus") || strings.Contains(line, "DBus") ||
			strings.Contains(line, "DevTools listening") {
			continue
		}
		slog.Debug("browser", "line", line)
	}
	return len(p), nil
}

// BrowserRelay pipes CDP between stdin/stdout and the slot's browser
// (its browser-level socket): newline-delimited JSON in both directions,
// after one hello line out (hosting.BrowserHello) once connected. The port runs
// it in the slot (Runtime.Exec) for the viewer. It ends when stdin closes
// or the browser side does. A failure to connect goes to stderr (the
// port shows it) with exit 1.
func BrowserRelay(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wsURL, err := browserSocket(ctx, browserURL())
	if err != nil {
		return err
	}
	return pipeCDP(ctx, wsURL, in, out)
}

// browserSocket is the browser-level CDP socket from /json/version.
func browserSocket(ctx context.Context, base string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", errx.Wrap(err, "the browser is not running", errx.TypeBusiness)
	}
	defer resp.Body.Close()
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v.WS == "" {
		return "", errx.Business("the browser answered no debugger URL")
	}
	return v.WS, nil
}
