package dockgithub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// ActionsPath is where a GitHub Actions workflow hands a message to a
// dock (POST ActionsPath+<dock id>, bearer = the job's OIDC token).
const ActionsPath = "/github/actions/"

// ActionSender is the sender handle of every message a workflow sends,
// besides @<actor>: customs rules can deny it (or not) as a whole.
const ActionSender = "github:action"

// ActionMessage is what a workflow posts.
type ActionMessage struct {
	// Text is the message (the agent's instructions).
	Text string `json:"text"`
	// Number is the issue or pull request it is about; 0: the pull
	// request of the workflow's ref (refs/pull/N/merge).
	Number int `json:"number,omitempty"`
	// Agent names the agent of the dock's team that should take it;
	// empty: routed like a message mentioning nobody.
	Agent string `json:"agent,omitempty"`
	// ID tells several messages of one run apart (default: a hash of the
	// text): a retried step with the same ID is not a second message.
	ID string `json:"id,omitempty"`
}

var actionID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func (m ActionMessage) Validate() error {
	if err := text.Required("text", m.Text, text.MaxMessage); err != nil {
		return err
	}
	if m.Number < 0 {
		return errx.InvalidField("number", "number must be positive")
	}
	if m.Agent != "" {
		if err := agent.ValidName(strings.TrimPrefix(m.Agent, "@")); err != nil {
			return errx.InvalidField("agent", "agent is not an agent name")
		}
	}
	if m.ID != "" && !actionID.MatchString(m.ID) {
		return errx.InvalidField("id", "id must be 1-64 letters, digits, '.', '_' or '-'")
	}
	return nil
}

// pullRef reads the pull request number of a refs/pull/N/merge ref.
var pullRef = regexp.MustCompile(`^refs/pull/([0-9]+)/(merge|head)$`)

// action takes a message from a GitHub Actions workflow of a repository
// the dock's App is installed on. The proof is the job's OIDC token,
// issued by GitHub for this port (audience = SEAPORT_PUBLIC_URL); no
// Seaport credential lives in the repository.
func (h *Handler) action(w http.ResponseWriter, r *http.Request) error {
	id, err := identity.ParseDockID(r.PathValue("dock"))
	if err != nil {
		return errx.NotFound("unknown dock")
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || bearer == "" {
		return errx.Unauthorized("a GitHub Actions OIDC token is required (permissions: id-token: write)")
	}
	a, err := h.g.store.App(r.Context(), id)
	if errx.IsType(err, errx.TypeNotFound) {
		return errx.NotFound("unknown dock")
	}
	if err != nil {
		return err
	}
	claims, err := h.g.oidc.verify(r.Context(), bearer, h.g.audience(), time.Now())
	if err != nil {
		return err
	}
	var in ActionMessage
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	repo := claims.Repository
	if !dock.ValidRepository(repo) {
		return errx.Unauthorized("invalid GitHub Actions token: bad repository")
	}
	// The repository must be one the dock's App serves now.
	inst, err := h.g.client.installationOf(r.Context(), a, repo)
	if errx.IsType(err, errx.TypeNotFound) {
		return errx.Forbidden("this dock's GitHub App is not installed on " + repo)
	}
	if err != nil {
		return errx.Wrap(err, "find the installation of "+repo, errx.TypeExternal)
	}
	installs, err := h.g.store.Installations(r.Context(), a.Dock)
	if err != nil {
		return err
	}
	if !activeInstallation(installs, inst) {
		return errx.Forbidden("this dock's GitHub App is not active on " + repo)
	}
	number := in.Number
	if number == 0 {
		if m := pullRef.FindStringSubmatch(claims.Ref); m != nil {
			number, _ = strconv.Atoi(m[1])
		}
	}
	if number <= 0 {
		return errx.InvalidField("number", "number is required outside a pull request workflow")
	}
	_, receiver, teams := h.g.bound()
	if receiver == nil || teams == nil {
		return errx.External("this port is starting; try again")
	}
	body := strings.TrimSpace(text.Clean(in.Text))
	if in.Agent != "" {
		name := strings.TrimPrefix(in.Agent, "@")
		team, err := teams.Team(r.Context(), a.Organization, a.Dock)
		if err != nil {
			return err
		}
		m, ok := agent.Mentioned(team, "@"+name)
		if !ok {
			return errx.InvalidField("agent", "no agent "+name+" on this dock")
		}
		if first, called := agent.Mentioned(team, body); !called || first.Agent != m.Agent {
			body = "@" + m.Name + " " + body
		}
	}
	key := in.ID
	if key == "" {
		sum := sha256.Sum256([]byte(body))
		key = hex.EncodeToString(sum[:8])
	}
	chat := repo + "#" + strconv.Itoa(number)
	owner, _, _ := strings.Cut(repo, "/")
	header := "(GitHub Actions: " + clip(claims.Workflow, 100) + ", run " + claims.RunID + ", " + claims.Event + ")"
	msg := ship.Inbound{
		Dock: a.Dock, Channel: dock.ChannelGitHub,
		ChatID: chat, ChatKind: conversation.KindGroup, ChatTitle: h.g.threadTitle(r.Context(), a, repo, number),
		ExternalID: "action-" + claims.RunID + "-" + claims.RunAttempt + "-" + key,
		SenderID:   "@" + claims.Actor, SenderName: claims.Actor,
		Text:          clip(header+"\n"+body, text.MaxMessage),
		SentAt:        time.Now(),
		ChatHandles:   []string{chat, repo, owner},
		SenderHandles: []string{"@" + claims.Actor, ActionSender},
		Addressed:     true,
	}
	st, created, err := receiver.Receive(r.Context(), a.Organization, msg)
	if err != nil {
		slog.Error("receive github action message", "dock", a.Dock, "chat", chat, "err", err)
		return err
	}
	return httpx.JSON(w, http.StatusAccepted, map[string]any{
		"status": st.Status, "ship_id": st.ID, "created": created, "conversation_id": st.Conversation, "chat": chat,
	})
}

// audience is what the workflow's OIDC token must be issued for: this
// port's public URL.
func (g *Gateway) audience() string { return strings.TrimRight(g.cfg.PublicURL, "/") }

// threadTitle names the conversation like webhooks do ("PR owner/repo#N:
// title"); "" when GitHub doesn't answer (the stored title stays).
func (g *Gateway) threadTitle(ctx context.Context, a App, repo string, number int) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tok, err := g.client.repoToken(ctx, a, repo)
	if err != nil {
		return ""
	}
	var issue struct {
		Title       string    `json:"title"`
		PullRequest *struct{} `json:"pull_request"`
	}
	if err := g.client.do(ctx, http.MethodGet, "/repos/"+repo+"/issues/"+strconv.Itoa(number), tok, nil, &issue); err != nil {
		return ""
	}
	label := "Issue"
	if issue.PullRequest != nil {
		label = "PR"
	}
	return clip(label+" "+repo+"#"+strconv.Itoa(number)+": "+issue.Title, text.MaxName)
}
