package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// writeApprovals tells the agent about its questions to people: the ones
// still pending and the ones answered (or ended) by this delivery's ships.
func writeApprovals(b *strings.Builder, approvals []ship.Approval) {
	if len(approvals) == 0 {
		return
	}
	b.WriteString("\nApprovals you asked (act only on an answered one; an expired or cancelled one means do not do it):\n")
	for _, a := range approvals {
		switch a.Status {
		case ship.ApprovalAnswered:
			answer := ""
			if a.Answer != nil {
				answer = *a.Answer
			}
			fmt.Fprintf(b, "- %s answered %q by %s: %s\n", a.ID, answer, a.AnsweredBy, a.Text)
		case ship.ApprovalPending:
			fmt.Fprintf(b, "- %s pending (options: %s): %s\n", a.ID, strings.Join(a.Options, " | "), a.Text)
		default:
			fmt.Fprintf(b, "- %s %s: %s\n", a.ID, a.Status, a.Text)
		}
	}
}

// approvalHint is what the prompt says of asking before consequential
// actions (only where the agent can post to people).
const approvalHint = "Before anything consequential or irreversible, ask people with the seaport `ask_approval` tool " +
	"(or `seaport ask <conversation_id> -option A -option B \"question\"`) and act only on the option they choose."

// AskCommand is `seaport ask <conversation-id> -option TEXT -option TEXT
// [-wait SECONDS] <question>`: an agent asks people to choose before it
// acts, as the agent of SEAPORT_CREW_TOKEN on SEAPORT_URL. It prints
// `<approval-id> <status> [answer]` (with -wait it blocks for the answer,
// at most ship.MaxApprovalWait; the status is pending when none came).
func AskCommand(ctx context.Context, args []string, out io.Writer) error {
	const usage = "usage: seaport ask <conversation-id> -option TEXT -option TEXT... [-wait SECONDS] <question>"
	if len(args) == 0 {
		return errx.Validation(usage)
	}
	conv, err := identity.ParseConversationID(args[0])
	if err != nil {
		return errx.Validation(usage + " (" + err.Error() + ")")
	}
	in := crew.AskRequest{ConversationID: conv}
	var words []string
	for i := 1; i < len(args); i++ {
		switch a := args[i]; a {
		case "-option", "--option", "-wait", "--wait":
			if i+1 == len(args) {
				return errx.Validation(usage)
			}
			i++
			if strings.HasSuffix(a, "option") {
				in.Options = append(in.Options, args[i])
				continue
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return errx.Validation(usage + " (-wait is a number of seconds)")
			}
			in.WaitSeconds = n
		default:
			words = append(words, a)
		}
	}
	in.Text = strings.Join(words, " ")
	if err := in.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(in)
	return callApprovals(ctx, http.MethodPost, crew.ApprovalsPath, raw, time.Duration(in.WaitSeconds)*time.Second, http.StatusCreated, out)
}

// ApprovalCommand is `seaport approval <approval-id> [-wait SECONDS]`: read
// an approval the agent asked, blocking while it is pending with -wait.
func ApprovalCommand(ctx context.Context, args []string, out io.Writer) error {
	const usage = "usage: seaport approval <approval-id> [-wait SECONDS]"
	if len(args) == 0 {
		return errx.Validation(usage)
	}
	id, err := identity.ParseApprovalID(args[0])
	if err != nil {
		return errx.Validation(usage + " (" + err.Error() + ")")
	}
	path, wait := crew.ApprovalsPath+"/"+id.String(), time.Duration(0)
	if len(args) == 3 && (args[1] == "-wait" || args[1] == "--wait") {
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 0 {
			return errx.Validation(usage)
		}
		path, wait = path+"?wait_seconds="+args[2], time.Duration(n)*time.Second
	} else if len(args) != 1 {
		return errx.Validation(usage)
	}
	return callApprovals(ctx, http.MethodGet, path, nil, wait, http.StatusOK, out)
}

func callApprovals(ctx context.Context, method, path string, body []byte, wait time.Duration, want int, out io.Writer) error {
	server := strings.TrimRight(os.Getenv(hosting.EnvURL), "/")
	token := os.Getenv(hosting.EnvToken)
	if server == "" || token == "" {
		return errx.Validation(hosting.EnvURL + " and " + hosting.EnvToken + " are required (the connector sets them for its agent)")
	}
	ctx, cancel := context.WithTimeout(ctx, wait+time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, server+path, bytes.NewReader(body))
	if err != nil {
		return errx.Wrap(err, "approval request", errx.TypeValidation)
	}
	req.Header = authHeader(token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errx.Wrap(err, "call the port", errx.TypeExternal)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != want {
		return errx.External("approval: " + portMessage(raw, resp.Status))
	}
	var a ship.Approval
	if err := json.Unmarshal(raw, &a); err != nil {
		return errx.External("the port's answer does not decode")
	}
	line := a.ID.String() + " " + string(a.Status)
	if a.Answer != nil {
		line += " " + *a.Answer
	}
	_, err = fmt.Fprintln(out, line)
	return err
}
