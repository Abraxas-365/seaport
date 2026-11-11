package bootstrap_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"go.yaml.in/yaml/v3"
)

// The composite action (action/action.yml) runs as a workflow step would:
// it asks the runner for an OIDC token for the port and posts the
// message; the agent gets it in the pull request's conversation.
func TestGitHubAction(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Using string `yaml:"using"`
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(raw, &action); err != nil || action.Runs.Using != "composite" || len(action.Runs.Steps) != 1 {
		t.Fatalf("action.yml: %v %+v", err, action)
	}
	step := action.Runs.Steps[0]

	gh := newFakeGitHub(t)
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "github-test-secret-key-0123456789abcdef"
		c.GitHubURL, c.GitHubAPIURL = gh.srv.URL, gh.srv.URL
		c.GitHubOIDCURL = gh.srv.URL + "/oidc"
	}, bootstrap.Options{})
	d := p.githubDock(gh)
	p.assignAs(adminToken, d.ID, p.agent(testAgent).ID, true)
	p.letIn(d)

	// The runner's token service: a token for the audience asked for.
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runner-request-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":"` + gh.actionToken(gh.oidcKey, r.URL.Query().Get("audience"), "acme/web", nil) + `"}`))
	}))
	defer runner.Close()

	inputs := map[string]string{"port-url": p.srv.URL + "/", "dock": d.ID.String(), "text": `QA "checkout" with a coupon`, "agent": "test", "number": "", "id": ""}
	output := filepath.Join(t.TempDir(), "output")
	run := func(withToken bool) (string, error) {
		cmd := exec.Command("bash", "-c", step.Run)
		cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+output)
		if withToken {
			cmd.Env = append(cmd.Env, "ACTIONS_ID_TOKEN_REQUEST_URL="+runner.URL+"/token?api-version=2.0", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=runner-request-token")
		}
		for k, v := range step.Env {
			name := strings.TrimSuffix(strings.TrimPrefix(v, "${{ inputs."), " }}")
			cmd.Env = append(cmd.Env, k+"="+inputs[name])
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(false); err == nil || !strings.Contains(out, "id-token: write") {
		t.Fatalf("without id-token permission: %v %s", err, out)
	}
	out, err := run(true)
	if err != nil {
		t.Fatalf("action: %v\n%s", err, out)
	}
	written, _ := os.ReadFile(output)
	if !strings.Contains(string(written), "status=queued") || !strings.Contains(string(written), "ship-id=") {
		t.Fatalf("outputs = %s (log %s)", written, out)
	}
	c := p.dialCrew(1)
	f := c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Conversation.ExternalID != "acme/web#7" || !strings.Contains(f.Delivery.Ships[0].Text, `@test QA "checkout" with a coupon`) {
		t.Fatalf("delivery = %+v", f.Delivery)
	}
	ack(c, f)
	// A dock that doesn't exist: the step fails with Seaport's message.
	inputs["dock"] = dock.Dock{}.ID.String()
	if out, err := run(true); err == nil || !strings.Contains(out, "Seaport answered 404") {
		t.Fatalf("unknown dock: %v %s", err, out)
	}
}
