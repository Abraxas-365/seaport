package connector

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// The virtual display headed Chromium draws on. Sites treat a headless
// Chromium as a bot far more readily than a headed one (Google's sign-in
// refuses a headless one at the first step), so the engine runs headed on
// Xvfb when the image has it (hosting.Dockerfile's browser stage installs
// it) and headless otherwise: an image built before Xvfb was part of the
// stage, until the environment is rebuilt (`seaport env build -force`).

// xvfb is the X server binary looked up on PATH.
var xvfb = "Xvfb"

// The virtual screen: Chromium's tab strip and address bar take about
// 144 px of a window this size, which leaves the page about 1280×800.
const (
	screenWidth  = 1280
	screenHeight = 944
)

// display is a running Xvfb.
type display struct {
	// name is the DISPLAY value (":N").
	name string
	cmd  *exec.Cmd
	done chan struct{}
}

// startDisplay starts Xvfb on a free display (it picks the number and
// writes it on -displayfd, so a stale lock of a killed one never collides)
// listening on no TCP and no socket file (only Linux's abstract socket:
// /tmp needs no .X11-unix). It returns nil, nil when the image has no
// Xvfb.
func startDisplay(ctx context.Context) (*display, error) {
	path, err := exec.LookPath(xvfb)
	if err != nil {
		return nil, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, errx.Wrap(err, "start the virtual display", errx.TypeInternal)
	}
	defer r.Close()
	geometry := strconv.Itoa(screenWidth) + "x" + strconv.Itoa(screenHeight) + "x24"
	cmd := exec.Command(path, "-displayfd", "3", "-screen", "0", geometry, "-nolisten", "tcp", "-nolisten", "unix")
	cmd.ExtraFiles = []*os.File{w} // fd 3
	cmd.Stdout = io.Discard
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		w.Close()
		return nil, errx.Wrap(err, "start the virtual display", errx.TypeInternal)
	}
	w.Close() // the child holds its own copy; EOF once it exits
	d := &display{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(d.done)
	}()
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		got <- strings.TrimSpace(line)
	}()
	select {
	case n := <-got:
		if _, err := strconv.Atoi(n); err != nil || n == "" {
			d.stop()
			return nil, errx.Internal("the virtual display did not start: " + stderr.String())
		}
		d.name = ":" + n
		return d, nil
	case <-time.After(browserReady):
		d.stop()
		return nil, errx.Internal("the virtual display did not start within " + browserReady.String())
	case <-ctx.Done():
		d.stop()
		return nil, ctx.Err()
	}
}

// stop ends the X server.
func (d *display) stop() {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return
	}
	endProcessGroup(d.cmd, d.done, 5*time.Second)
}

// tailBuffer keeps the last bytes written to it (a failure's message).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 512 {
		t.buf = t.buf[len(t.buf)-512:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// chromiumArgs are the engine's flags for profile: headed (on a virtual
// display, the window filling the screen) or headless.
func chromiumArgs(profile string, port int, headed bool) []string {
	args := []string{
		"--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--disable-crash-reporter", "--no-crashpad", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--disable-sync", "--disable-features=Translate",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=" + strconv.Itoa(port),
		"--remote-allow-origins=*", "--user-data-dir=" + profile,
		"--lang=en-US", // sites render in a predictable language for the agent
	}
	if headed {
		return append(args, "--window-position=0,0",
			"--window-size="+strconv.Itoa(screenWidth)+","+strconv.Itoa(screenHeight))
	}
	return append(args, "--headless=new", "--window-size=1280,800")
}
