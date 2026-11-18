//go:build !windows

package connector

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeXvfb points startDisplay at a script for the test.
func fakeXvfb(t *testing.T, script string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Xvfb")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := xvfb
	xvfb = p
	t.Cleanup(func() { xvfb = old })
}

// startDisplay: no Xvfb is headless (nil, no error); a running one is
// read from -displayfd and stopped with its group; one that exits without
// a display number is an error with its stderr.
func TestStartDisplay(t *testing.T) {
	ctx := context.Background()
	old := xvfb
	xvfb = filepath.Join(t.TempDir(), "no-such-xvfb")
	if d, err := startDisplay(ctx); d != nil || err != nil {
		t.Fatalf("no Xvfb: %v %v", d, err)
	}
	xvfb = old

	args := filepath.Join(t.TempDir(), "args")
	fakeXvfb(t, `echo "$@" > `+args+"\necho 7 >&3\nexec sleep 30\n")
	d, err := startDisplay(ctx)
	if err != nil || d == nil || d.name != ":7" {
		t.Fatalf("display: %+v %v", d, err)
	}
	d.stop()
	select {
	case <-d.done:
	default:
		t.Fatal("stop left the X server running")
	}
	got, _ := os.ReadFile(args)
	for _, want := range []string{"-displayfd 3", "-screen 0 1280x944x24", "-nolisten tcp", "-nolisten unix"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("Xvfb args %q lack %q", got, want)
		}
	}
	var nilDisplay *display
	nilDisplay.stop()

	fakeXvfb(t, "echo 'cannot open log' >&2\nexit 1\n")
	if d, err := startDisplay(ctx); d != nil || err == nil || !strings.Contains(err.Error(), "cannot open log") {
		t.Fatalf("failed Xvfb: %+v %v", d, err)
	}
}

// Headed Chromium fills the virtual screen; headless keeps its own
// window; both keep CDP on loopback with the profile.
func TestChromiumArgs(t *testing.T) {
	headed, headless := chromiumArgs("/p", 9222, true), chromiumArgs("/p", 9222, false)
	for _, a := range [][]string{headed, headless} {
		for _, want := range []string{"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=9222", "--user-data-dir=/p", "--no-sandbox"} {
			if !slices.Contains(a, want) {
				t.Errorf("%v lacks %s", a, want)
			}
		}
	}
	if slices.Contains(headed, "--headless=new") || !slices.Contains(headed, "--window-size=1280,944") || !slices.Contains(headed, "--window-position=0,0") {
		t.Errorf("headed: %v", headed)
	}
	if !slices.Contains(headless, "--headless=new") || !slices.Contains(headless, "--window-size=1280,800") {
		t.Errorf("headless: %v", headless)
	}
}
