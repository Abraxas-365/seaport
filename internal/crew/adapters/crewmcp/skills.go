package crewmcp

import (
	"context"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addSkill adds the skill tool when the calling agent's current profile
// has skills: its description lists them (name: when to use it), a call
// returns one's instructions. Every harness gets skills this way, without
// files on the agent's machine; a changed profile shows on the next
// request (the server is per request).
func (t tools) addSkill(ctx context.Context, srv *mcp.Server) {
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok || p.Agent.IsZero() {
		return
	}
	profile, err := t.d.Agents.Profile(ctx, p.Organization, p.Agent, 0)
	if err != nil || len(profile.Skills) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("Load a skill: instructions for a kind of task. Before doing a task that matches one of these skills, call this tool with its name and follow what it returns.\n\nSkills:\n")
	for _, s := range profile.Skills {
		b.WriteString("- " + s.Name + ": " + s.Description + "\n")
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "skill", Description: b.String()}, t.skill)
}

type SkillIn struct {
	Name string `json:"name" jsonschema:"the skill's name, from the list in this tool's description"`
}

type SkillOut struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}

func (t tools) skill(ctx context.Context, _ *mcp.CallToolRequest, in SkillIn) (*mcp.CallToolResult, SkillOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), SkillOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Validation("no agent: skills belong to an agent's profile")), SkillOut{}, nil
	}
	profile, err := t.d.Agents.Profile(ctx, org, *agentID, 0)
	if err != nil {
		return failure(err), SkillOut{}, nil
	}
	name := strings.TrimSpace(in.Name)
	for _, s := range profile.Skills {
		if s.Name == name {
			return nil, SkillOut{Name: s.Name, Instructions: s.Body}, nil
		}
	}
	return failure(errx.NotFound("no skill named " + name)), SkillOut{}, nil
}
