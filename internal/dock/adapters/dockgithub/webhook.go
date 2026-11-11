package dockgithub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// validSignature checks X-Hub-Signature-256 (HMAC-SHA256 of the body
// with the App's webhook secret).
func validSignature(secret, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// user is a GitHub account in a payload.
type user struct {
	Login string `json:"login"`
	Type  string `json:"type"` // User | Bot | Organization
}

// post is what comments, reviews, issues and pull requests share.
type post struct {
	ID                int64     `json:"id"`
	Number            int       `json:"number"`
	Title             string    `json:"title"`
	Body              string    `json:"body"`
	User              user      `json:"user"`
	AuthorAssociation string    `json:"author_association"`
	CreatedAt         time.Time `json:"created_at"`
	SubmittedAt       time.Time `json:"submitted_at"`
	State             string    `json:"state"`
	Path              string    `json:"path"`
	Line              int       `json:"line"`
	// PullRequest is set on issues that are pull requests.
	PullRequest *struct{} `json:"pull_request"`
}

// event is a webhook payload (the fields Seaport reads).
type event struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender      user  `json:"sender"`
	Comment     *post `json:"comment"`
	Review      *post `json:"review"`
	Issue       *post `json:"issue"`
	PullRequest *post `json:"pull_request"`
}

// arrival is one webhook turned into a message, plus how to mark it seen.
type arrival struct {
	in ship.Inbound
	// reactPath is where the 👀 reaction goes ("": nowhere, e.g. reviews).
	reactPath string
	repo      string
}

// members are the author associations that count as the repository's people.
var members = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

// parse turns a webhook into an arrival, or ok=false for events that
// carry no message for the crew: other kinds and actions, bots (our own
// comments included, so a reply never wakes anyone), empty texts and
// messages that call neither the App nor an agent of the dock's team.
func parse(kind string, raw []byte, d identity.DockID, slug string, team []agent.Member) (arrival, bool) {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil || e.Repository.FullName == "" {
		return arrival{}, false
	}
	var thread, msg *post
	var id, body, react string
	pr := false
	switch {
	case kind == "issue_comment" && e.Action == "created" && e.Issue != nil && e.Comment != nil:
		thread, msg, pr = e.Issue, e.Comment, e.Issue.PullRequest != nil
		id, body = "comment-"+itoa(msg.ID), msg.Body
		react = "/issues/comments/" + itoa(msg.ID)
	case kind == "pull_request_review_comment" && e.Action == "created" && e.PullRequest != nil && e.Comment != nil:
		thread, msg, pr = e.PullRequest, e.Comment, true
		id, body = "review-comment-"+itoa(msg.ID), msg.Body
		if msg.Path != "" {
			where := msg.Path
			if msg.Line > 0 {
				where += ":" + strconv.Itoa(msg.Line)
			}
			body = "(on " + where + ") " + body
		}
		react = "/pulls/comments/" + itoa(msg.ID)
	case kind == "pull_request_review" && e.Action == "submitted" && e.PullRequest != nil && e.Review != nil:
		thread, msg, pr = e.PullRequest, e.Review, true
		id, body = "review-"+itoa(msg.ID), msg.Body
		if msg.State != "" && body != "" {
			body = "(review: " + strings.ToLower(msg.State) + ") " + body
		}
		msg.CreatedAt = msg.SubmittedAt
	case kind == "issues" && e.Action == "opened" && e.Issue != nil:
		thread, msg = e.Issue, e.Issue
		id, body = "issue-"+itoa(msg.ID), opened(msg)
		react = "/issues/" + strconv.Itoa(msg.Number)
	case kind == "pull_request" && e.Action == "opened" && e.PullRequest != nil:
		thread, msg, pr = e.PullRequest, e.PullRequest, true
		id, body = "pr-"+itoa(msg.ID), opened(msg)
		react = "/issues/" + strconv.Itoa(msg.Number)
	default:
		return arrival{}, false
	}
	author := msg.User
	if author.Login == "" {
		author = e.Sender
	}
	if author.Type == "Bot" || e.Sender.Type == "Bot" || strings.TrimSpace(body) == "" || thread.Number <= 0 {
		return arrival{}, false
	}
	if !calls(body, slug, team) {
		return arrival{}, false
	}
	repo := e.Repository.FullName
	chat := repo + "#" + strconv.Itoa(thread.Number)
	label := "Issue"
	if pr {
		label = "PR"
	}
	title := clip(label+" "+chat+": "+thread.Title, text.MaxName)
	association := strings.ToUpper(msg.AuthorAssociation)
	people := "github:outsider"
	if members[association] {
		people = "github:member"
	}
	senders := []string{"@" + author.Login, people}
	if association != "" {
		senders = append(senders, "association:"+strings.ToLower(association))
	}
	owner, _, _ := strings.Cut(repo, "/")
	sent := msg.CreatedAt
	if sent.IsZero() {
		sent = time.Now()
	}
	return arrival{repo: repo, reactPath: react, in: ship.Inbound{
		Dock: d, Channel: dock.ChannelGitHub,
		ChatID: chat, ChatKind: conversation.KindGroup, ChatTitle: title,
		ExternalID: id, SenderID: "@" + author.Login, SenderName: author.Login,
		Text: clip(body, text.MaxMessage), SentAt: sent,
		ChatHandles:   []string{chat, repo, owner},
		SenderHandles: senders,
		Addressed:     true,
	}}, true
}

// opened renders a new issue or pull request as a message.
func opened(p *post) string {
	if strings.TrimSpace(p.Body) == "" {
		return p.Title
	}
	return p.Title + "\n\n" + p.Body
}

// calls: the text mentions the App (@slug or @slug[bot]) or an agent of
// the dock's team.
func calls(body, slug string, team []agent.Member) bool {
	if _, ok := agent.Mentioned(team, body); ok {
		return true
	}
	if slug == "" {
		return false
	}
	return mentions(strings.ToLower(body), strings.ToLower(slug))
}

// mentions: "@name" appears in lower, not inside a longer handle or an
// address (user@name).
func mentions(lower, name string) bool {
	at := "@" + name
	for i := strings.Index(lower, at); i >= 0; {
		end := i + len(at)
		before := i == 0 || !handleChar(lower[i-1])
		after := end == len(lower) || !handleChar(lower[end]) || strings.HasPrefix(lower[end:], "[bot]")
		if before && after {
			return true
		}
		next := strings.Index(lower[i+1:], at)
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return false
}

func handleChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

// clip cuts s to at most max runes.
func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// splitChat parses a conversation id "owner/repo#N".
func splitChat(chat string) (repo string, number int, ok bool) {
	repo, num, found := strings.Cut(chat, "#")
	if !found || strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return repo, n, true
}
