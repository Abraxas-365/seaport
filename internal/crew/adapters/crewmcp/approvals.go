package crewmcp

import (
	"context"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type AskIn struct {
	ConversationID string   `json:"conversation_id" jsonschema:"the conversation_id given in the seaport prompt"`
	Text           string   `json:"text" jsonschema:"the question: what you are about to do and why you need a person to decide"`
	Options        []string `json:"options" jsonschema:"2 to 6 short choices (at most 64 characters, none starting with a digit), e.g. [\"Approve\", \"Reject\"]"`
	WaitSeconds    int      `json:"wait_seconds,omitempty" jsonschema:"block up to this many seconds (at most 290) for the answer; 0 returns at once"`
}

type ApprovalIn struct {
	ApprovalID  string `json:"approval_id" jsonschema:"the id ask_approval returned"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"block up to this many seconds (at most 290) while it is pending"`
}

// ApprovalOut is the approval as the agent reads it. Decision is what to
// act on: the chosen option once answered, else empty.
type ApprovalOut struct {
	ApprovalID string   `json:"approval_id"`
	Status     string   `json:"status" jsonschema:"pending | answered | expired | cancelled"`
	Answer     string   `json:"answer,omitempty" jsonschema:"the option people chose, when answered"`
	AnsweredBy string   `json:"answered_by,omitempty"`
	Options    []string `json:"options"`
	ExpiresAt  string   `json:"expires_at"`
}

func approvalOut(a ship.Approval) ApprovalOut {
	out := ApprovalOut{ApprovalID: a.ID.String(), Status: string(a.Status), AnsweredBy: a.AnsweredBy,
		Options: a.Options, ExpiresAt: a.ExpiresAt.UTC().Format(time.RFC3339)}
	if a.Answer != nil {
		out.Answer = *a.Answer
	}
	return out
}

func (t tools) askApproval(ctx context.Context, _ *mcp.CallToolRequest, in AskIn) (*mcp.CallToolResult, ApprovalOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), ApprovalOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Forbidden("approvals need an agent's crew token")), ApprovalOut{}, nil
	}
	conv, err := identity.ParseConversationID(strings.TrimSpace(in.ConversationID))
	if err != nil {
		return failure(err), ApprovalOut{}, nil
	}
	// AskApproval refuses a conversation of a dock the agent doesn't work on.
	a, err := t.d.Ships.AskApproval(ctx, org, ship.AskApproval{Conversation: conv, Agent: *agentID, Text: in.Text,
		Options: in.Options, Wait: time.Duration(in.WaitSeconds) * time.Second})
	if err != nil {
		return failure(err), ApprovalOut{}, nil
	}
	return nil, approvalOut(a), nil
}

func (t tools) approval(ctx context.Context, _ *mcp.CallToolRequest, in ApprovalIn) (*mcp.CallToolResult, ApprovalOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), ApprovalOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Forbidden("approvals need an agent's crew token")), ApprovalOut{}, nil
	}
	id, err := identity.ParseApprovalID(strings.TrimSpace(in.ApprovalID))
	if err != nil {
		return failure(err), ApprovalOut{}, nil
	}
	cur, err := t.d.Reads.FindApproval(ctx, org, id)
	if err != nil {
		return failure(err), ApprovalOut{}, nil
	}
	if cur.Agent != *agentID {
		return failure(errx.NotFound("approval not found")), ApprovalOut{}, nil
	}
	a, err := t.d.Ships.AwaitApproval(ctx, org, id, time.Duration(in.WaitSeconds)*time.Second)
	if err != nil {
		return failure(err), ApprovalOut{}, nil
	}
	return nil, approvalOut(a), nil
}
