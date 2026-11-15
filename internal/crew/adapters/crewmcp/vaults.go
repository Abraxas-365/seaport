package crewmcp

import (
	"context"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addVaults adds the memory tools when the calling agent has any vault,
// through its own grants or those of any dock it works (vault.Queries.AnyFor;
// like skill and open, an agent with none sees no vault tools). Which vaults
// it may use in a call is decided per call: its own everywhere, a dock's
// only when the call names a conversation of that dock
// (vault.Queries.AccessFor), the widest mode winning. Writes go only through
// vault.Writer, which checks the grant (TestVaultWritesCheckAccess).
func (t tools) addVaults(ctx context.Context, srv *mcp.Server) {
	if t.d.Vaults == nil || t.d.VaultWrites == nil {
		return
	}
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok || p.Agent.IsZero() {
		return
	}
	if any, err := t.d.Vaults.AnyFor(ctx, p.Organization, p.Agent); err != nil || !any {
		return
	}
	const scope = " Your own vaults are usable everywhere; the vaults of a dock only while you work a conversation of that dock: " +
		"pass conversation_id (the one in the seaport prompt)."
	mcp.AddTool(srv, &mcp.Tool{
		Name: "vaults",
		Description: "List the vaults (shared memory of the organization) you can use here: name, what it is for, your mode (read | write) " +
			"and an index of its entries (name and first line, newest first)." + scope,
	}, t.vaults)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "memory_search",
		Description: "Search the entries of your vaults by words (all of them, or one with vault). Each hit names the vault, the entry, " +
			"a snippet and who wrote it. Search before answering about the company, a customer or a past decision." + scope,
	}, t.memorySearch)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "memory_read",
		Description: "Read one entry of a vault in full, with its version and who wrote it (weigh what an agent wrote from a chat " +
			"differently from what a person wrote)." + scope,
	}, t.memoryRead)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "memory_save",
		Description: "Save a markdown entry in a vault you can write: a durable fact worth keeping, one fact per entry, no secrets, " +
			"nothing transient. To create, omit base; to edit an existing entry, read it and pass its version as base (a conflict " +
			"means someone saved meanwhile: read it again). Entries holding something that looks like a credential are refused." + scope,
	}, t.memorySave)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_delete",
		Description: "Delete an entry of a vault you can write. It is gone for the agents (its past versions stay on record for people); prefer saving a corrected entry over deleting." + scope,
	}, t.memoryDelete)
}

// vaultScope parses the optional conversation_id of a call.
func vaultScope(raw string) (*identity.ConversationID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := identity.ParseConversationID(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// vaultCaller is the organization and agent of a vault call (open mode has
// no agent: vaults are an agent's).
func vaultCaller(ctx context.Context) (identity.OrganizationID, identity.AgentID, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return org, identity.AgentID{}, err
	}
	if agentID == nil {
		return org, identity.AgentID{}, errx.Forbidden("vaults belong to agents: connect with an agent's token")
	}
	return org, *agentID, nil
}

// Source says who wrote an entry, readable by the model.
type Source struct {
	Kind         string `json:"kind"`
	By           string `json:"by"`
	Conversation string `json:"conversation_id,omitempty"`
	Description  string `json:"description"`
}

// sourcer describes who wrote entries within one call, asking for each
// dock's channel once (an index names up to vault.MaxIndex entries).
type sourcer struct {
	t        tools
	org      identity.OrganizationID
	channels map[identity.DockID]string
}

func (t tools) sourcer(org identity.OrganizationID) *sourcer {
	return &sourcer{t: t, org: org, channels: map[identity.DockID]string{}}
}

func (s *sourcer) channel(ctx context.Context, id identity.DockID) string {
	if kind, ok := s.channels[id]; ok {
		return kind
	}
	kind := ""
	if s.t.d.Docks != nil {
		if d, err := s.t.d.Docks.Find(ctx, s.org, id); err == nil {
			kind = string(d.Channel) + " "
		}
	}
	s.channels[id] = kind
	return kind
}

func (s *sourcer) of(ctx context.Context, p vault.Provenance) Source {
	out := Source{Kind: string(p.Kind), By: p.By}
	desc := "written by a person"
	if p.Kind == vault.WrittenByAgent {
		desc = "written by agent"
	}
	if p.By != "" {
		desc += " " + p.By
	}
	if p.Conversation != nil {
		out.Conversation = p.Conversation.String()
		kind := ""
		if p.Dock != nil {
			kind = s.channel(ctx, *p.Dock)
		}
		desc += " from a " + kind + "conversation"
	}
	out.Description = desc
	return out
}

// VaultInfo is a vault the agent can use, with its index.
type VaultInfo struct {
	Name        string       `json:"name"`
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Mode        vault.Mode   `json:"mode"`
	Entries     int          `json:"entries"`
	Index       []IndexEntry `json:"index"`
}

type IndexEntry struct {
	Name      string    `json:"name"`
	FirstLine string    `json:"first_line"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    Source    `json:"source"`
}

type VaultsIn struct {
	ConversationID string `json:"conversation_id,omitempty" jsonschema:"the conversation you are working, to include its dock's vaults"`
}

type VaultsOut struct {
	Vaults []VaultInfo `json:"vaults"`
}

func (t tools) vaults(ctx context.Context, _ *mcp.CallToolRequest, in VaultsIn) (*mcp.CallToolResult, VaultsOut, error) {
	org, agentID, err := vaultCaller(ctx)
	if err != nil {
		return failure(err), VaultsOut{}, nil
	}
	conv, err := vaultScope(in.ConversationID)
	if err != nil {
		return failure(err), VaultsOut{}, nil
	}
	access, err := t.d.Vaults.AccessFor(ctx, org, agentID, conv)
	if err != nil {
		return failure(err), VaultsOut{}, nil
	}
	out := VaultsOut{Vaults: []VaultInfo{}}
	who := t.sourcer(org)
	for _, a := range access {
		idx, err := t.d.Vaults.Index(ctx, org, a.Vault.ID, vault.MaxIndex)
		if err != nil {
			return failure(err), VaultsOut{}, nil
		}
		info := VaultInfo{Name: a.Vault.Name, Title: a.Vault.Title, Description: a.Vault.Description, Mode: a.Mode,
			Entries: a.Vault.Entries, Index: make([]IndexEntry, 0, len(idx))}
		for _, s := range idx {
			info.Index = append(info.Index, IndexEntry{Name: s.Name, FirstLine: s.FirstLine, UpdatedAt: s.UpdatedAt, Source: who.of(ctx, s.Provenance)})
		}
		out.Vaults = append(out.Vaults, info)
	}
	return nil, out, nil
}

// resolve finds the named vault among those the agent can use in this call.
func (t tools) resolve(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, conv *identity.ConversationID, name string) (vault.Access, error) {
	access, err := t.d.Vaults.AccessFor(ctx, org, agentID, conv)
	if err != nil {
		return vault.Access{}, err
	}
	name = strings.TrimSpace(name)
	for _, a := range access {
		if a.Vault.Name == name {
			return a, nil
		}
	}
	return vault.Access{}, errx.NotFound("no vault named " + name + " is available to you here (vaults tool lists them; a dock's vaults need conversation_id)")
}

type SearchIn struct {
	Query          string `json:"query" jsonschema:"the words to look for"`
	Vault          string `json:"vault,omitempty" jsonschema:"search only this vault (default: all you can use)"`
	ConversationID string `json:"conversation_id,omitempty"`
}

type HitOut struct {
	Vault     string    `json:"vault"`
	Name      string    `json:"name"`
	Snippet   string    `json:"snippet"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    Source    `json:"source"`
}

type SearchOut struct {
	Hits []HitOut `json:"hits"`
}

func (t tools) memorySearch(ctx context.Context, _ *mcp.CallToolRequest, in SearchIn) (*mcp.CallToolResult, SearchOut, error) {
	org, agentID, err := vaultCaller(ctx)
	if err != nil {
		return failure(err), SearchOut{}, nil
	}
	conv, err := vaultScope(in.ConversationID)
	if err != nil {
		return failure(err), SearchOut{}, nil
	}
	access, err := t.d.Vaults.AccessFor(ctx, org, agentID, conv)
	if err != nil {
		return failure(err), SearchOut{}, nil
	}
	var ids []identity.VaultID
	for _, a := range access {
		if in.Vault == "" || a.Vault.Name == strings.TrimSpace(in.Vault) {
			ids = append(ids, a.Vault.ID)
		}
	}
	if in.Vault != "" && len(ids) == 0 {
		return failure(errx.NotFound("no vault named " + in.Vault + " is available to you here")), SearchOut{}, nil
	}
	hits, err := t.d.Vaults.Search(ctx, org, ids, in.Query, 0)
	if err != nil {
		return failure(err), SearchOut{}, nil
	}
	out := SearchOut{Hits: make([]HitOut, 0, len(hits))}
	who := t.sourcer(org)
	for _, h := range hits {
		out.Hits = append(out.Hits, HitOut{Vault: h.VaultName, Name: h.Name, Snippet: h.Snippet, Version: h.Version,
			UpdatedAt: h.UpdatedAt, Source: who.of(ctx, h.Provenance)})
	}
	return nil, out, nil
}

type ReadIn struct {
	Vault          string `json:"vault" jsonschema:"the vault's name"`
	Name           string `json:"name" jsonschema:"the entry's name"`
	ConversationID string `json:"conversation_id,omitempty"`
}

type EntryOut struct {
	Vault     string    `json:"vault"`
	Name      string    `json:"name"`
	Body      string    `json:"body,omitempty"`
	Version   int       `json:"version"`
	Mode      string    `json:"mode,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    Source    `json:"source"`
}

func (t tools) memoryRead(ctx context.Context, _ *mcp.CallToolRequest, in ReadIn) (*mcp.CallToolResult, EntryOut, error) {
	org, agentID, err := vaultCaller(ctx)
	if err != nil {
		return failure(err), EntryOut{}, nil
	}
	conv, err := vaultScope(in.ConversationID)
	if err != nil {
		return failure(err), EntryOut{}, nil
	}
	a, err := t.resolve(ctx, org, agentID, conv, in.Vault)
	if err != nil {
		return failure(err), EntryOut{}, nil
	}
	e, err := t.d.Vaults.EntryByName(ctx, org, a.Vault.ID, strings.TrimSpace(in.Name))
	if err != nil {
		return failure(err), EntryOut{}, nil
	}
	return nil, EntryOut{Vault: a.Vault.Name, Name: e.Name, Body: e.Body, Version: e.Version, Mode: string(a.Mode),
		UpdatedAt: e.UpdatedAt, Source: t.sourcer(org).of(ctx, e.Provenance)}, nil
}

type SaveIn struct {
	Vault          string `json:"vault" jsonschema:"the vault's name (you need write mode on it here)"`
	Name           string `json:"name" jsonschema:"the entry's name: lowercase letters, digits, - or _"`
	Body           string `json:"body" jsonschema:"the markdown text of the entry"`
	Base           int    `json:"base,omitempty" jsonschema:"the version you read and are editing; omit to create a new entry"`
	ConversationID string `json:"conversation_id,omitempty"`
}

func (t tools) memorySave(ctx context.Context, _ *mcp.CallToolRequest, in SaveIn) (*mcp.CallToolResult, EntryOut, error) {
	org, agentID, err := vaultCaller(ctx)
	if err != nil {
		return failure(err), EntryOut{}, nil
	}
	conv, err := vaultScope(in.ConversationID)
	if err != nil {
		return failure(err), EntryOut{}, nil
	}
	a, err := t.resolve(ctx, org, agentID, conv, in.Vault)
	if err != nil {
		return failure(err), EntryOut{}, nil
	}
	// WriteAs checks the write grant (and fills the provenance).
	e, err := t.d.VaultWrites.WriteAs(ctx, org, agentID, conv, vault.SaveEntry{Vault: a.Vault.ID, Name: strings.TrimSpace(in.Name), Body: in.Body, Base: in.Base})
	if err != nil {
		return failure(err), EntryOut{}, nil
	}
	return nil, EntryOut{Vault: a.Vault.Name, Name: e.Name, Version: e.Version, Mode: string(a.Mode),
		UpdatedAt: e.UpdatedAt, Source: t.sourcer(org).of(ctx, e.Provenance)}, nil
}

type DeleteIn struct {
	Vault          string `json:"vault"`
	Name           string `json:"name"`
	ConversationID string `json:"conversation_id,omitempty"`
}

type DeleteOut struct {
	Deleted string `json:"deleted"`
	Version int    `json:"last_version"`
}

func (t tools) memoryDelete(ctx context.Context, _ *mcp.CallToolRequest, in DeleteIn) (*mcp.CallToolResult, DeleteOut, error) {
	org, agentID, err := vaultCaller(ctx)
	if err != nil {
		return failure(err), DeleteOut{}, nil
	}
	conv, err := vaultScope(in.ConversationID)
	if err != nil {
		return failure(err), DeleteOut{}, nil
	}
	a, err := t.resolve(ctx, org, agentID, conv, in.Vault)
	if err != nil {
		return failure(err), DeleteOut{}, nil
	}
	e, err := t.d.VaultWrites.DeleteAs(ctx, org, agentID, conv, a.Vault.ID, strings.TrimSpace(in.Name))
	if err != nil {
		return failure(err), DeleteOut{}, nil
	}
	return nil, DeleteOut{Deleted: e.Name, Version: e.Version}, nil
}
