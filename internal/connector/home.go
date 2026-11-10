package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// seaportRequire is the line Seaport's rness base puts first in init.lua
// (images/Dockerfile): it routes models through the port's gateway.
const seaportRequire = `require("seaport") -- Seaport: models through the port's gateway (keep this line)`

// officialMarketplace is Claude Code's own plugin marketplace (plugins
// named @claude-plugins-official need no declaration).
const (
	officialMarketplace       = "claude-plugins-official"
	officialMarketplaceSource = "anthropics/claude-plugins-official"
)

// HomeSetup finishes an environment's harness home while its image is
// built (`seaport home-setup`, after the home is copied in; as the agent
// user, with the build's network): rness keeps Seaport's require line
// first in init.lua; Claude Code installs the plugins settings.json
// enables, from the marketplaces it declares.
func HomeSetup(ctx context.Context, harness, home string, log io.Writer) error {
	switch harness {
	case hosting.HarnessRness:
		return keepSeaportRequire(filepath.Join(home, ".rness", "init.lua"))
	case hosting.HarnessClaudeCode:
		return installPlugins(ctx, filepath.Join(home, ".claude", "settings.json"), log)
	}
	return errx.Validation("home-setup runs in a Seaport base image (SEAPORT_HARNESS is " + harness + ")")
}

// keepSeaportRequire puts Seaport's require line first in init.lua (an
// uploaded init.lua may lack it, or load it after declaring providers).
func keepSeaportRequire(path string) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errx.Wrap(err, "read init.lua", errx.TypeInternal)
	}
	lines := strings.Split(string(raw), "\n")
	kept := []string{seaportRequire}
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, `require("seaport")`) || strings.HasPrefix(t, `require "seaport"`) ||
			strings.HasPrefix(t, `require('seaport')`) {
			continue
		}
		kept = append(kept, l)
	}
	out := strings.Join(kept, "\n")
	if out == string(raw) {
		return nil
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return errx.Wrap(err, "write init.lua", errx.TypeInternal)
	}
	return nil
}

// claudeSettings is what installPlugins reads of settings.json.
type claudeSettings struct {
	EnabledPlugins         map[string]json.RawMessage `json:"enabledPlugins"`
	ExtraKnownMarketplaces map[string]struct {
		Source marketplaceSource `json:"source"`
	} `json:"extraKnownMarketplaces"`
}

type marketplaceSource struct {
	Source string `json:"source"`
	Repo   string `json:"repo"`
	URL    string `json:"url"`
	Path   string `json:"path"`
}

// arg is the source as `claude plugin marketplace add` takes it.
func (s marketplaceSource) arg() (string, error) {
	switch s.Source {
	case "github":
		if s.Repo != "" {
			return s.Repo, nil
		}
	case "git", "url":
		if strings.HasPrefix(s.URL, "https://") {
			return s.URL, nil
		}
	case "directory", "file":
		return "", errx.Validation("a " + s.Source + " marketplace (" + s.Path + ") is one machine's: publish it in a git repository")
	}
	return "", errx.Validation("unsupported marketplace source " + s.Source + " (github, git or url, over https)")
}

// pluginPlan is what to install: marketplace sources (name → source)
// and plugins (name@marketplace), both sorted.
type pluginPlan struct {
	marketplaces [][2]string
	plugins      []string
}

// planPlugins reads which plugins settings enables and the marketplaces
// they come from (declared, or Claude Code's own).
func planPlugins(raw []byte) (pluginPlan, error) {
	var s claudeSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return pluginPlan{}, errx.Validation("settings.json is not valid JSON: " + err.Error())
	}
	var plan pluginPlan
	needed := map[string]bool{}
	for id, v := range s.EnabledPlugins {
		var on bool
		if json.Unmarshal(v, &on) != nil || !on {
			continue
		}
		name, market, ok := strings.Cut(id, "@")
		if !ok || name == "" || market == "" {
			return pluginPlan{}, errx.Validation("enabledPlugins: " + id + " must be plugin@marketplace")
		}
		plan.plugins = append(plan.plugins, id)
		needed[market] = true
	}
	for market := range needed {
		src := officialMarketplaceSource
		if m, ok := s.ExtraKnownMarketplaces[market]; ok {
			arg, err := m.Source.arg()
			if err != nil {
				return pluginPlan{}, errx.Validation("marketplace " + market + ": " + err.Error())
			}
			src = arg
		} else if market != officialMarketplace {
			return pluginPlan{}, errx.Validation("marketplace " + market + " is not declared: add it to extraKnownMarketplaces")
		}
		plan.marketplaces = append(plan.marketplaces, [2]string{market, src})
	}
	slices.Sort(plan.plugins)
	slices.SortFunc(plan.marketplaces, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	return plan, nil
}

// installPlugins installs the plugins settings.json enables (a failed
// install fails the build: the slot would run without them).
func installPlugins(ctx context.Context, settings string, log io.Writer) error {
	raw, err := os.ReadFile(settings)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errx.Wrap(err, "read settings.json", errx.TypeInternal)
	}
	plan, err := planPlugins(raw)
	if err != nil {
		return err
	}
	claude := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "claude", append([]string{"plugin"}, args...)...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = io.MultiWriter(log, &out), io.MultiWriter(log, &out)
		if err := cmd.Run(); err != nil {
			return errx.Business("claude plugin " + strings.Join(args, " ") + " failed: " + lastLine(out.String()))
		}
		return nil
	}
	for _, m := range plan.marketplaces {
		if err := claude("marketplace", "add", m[1]); err != nil {
			return err
		}
	}
	for _, p := range plan.plugins {
		if err := claude("install", p); err != nil {
			return err
		}
	}
	return nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
