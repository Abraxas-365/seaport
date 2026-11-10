package bootstrap_test

import (
	"go/ast"
	"path"
	"sort"
	"testing"
)

// Tenancy rule (AGENTS.md "Rules"; ~/.rness/plans/puerto/modes-and-tenancy.md):
// Seaport is multi-tenant inside whatever the run mode, so every method of a
// tenant domain's (dock, conversation, ship, crew, agent, hosting) Commands, Queries, CustomsCommands, CustomsQueries,
// Messenger, Repository, CustomsRepository, Token*, Presence, DockLocker,
// Owners and Relay port takes
// `organization identity.OrganizationID` right after ctx. A method that is
// genuinely system-wide must be listed in systemWide with the reason.

// tenantDomains own tenant rows (organization_id).
var tenantDomains = map[string]bool{"dock": true, "conversation": true, "ship": true, "crew": true, "agent": true, "hosting": true}

// tenantPorts are the port interfaces the rule covers.
var tenantPorts = map[string]bool{
	"Commands": true, "Queries": true, "Messenger": true, "Repository": true,
	"CustomsCommands": true, "CustomsQueries": true, "CustomsRepository": true,
	"TokenCommands": true, "TokenQueries": true, "TokenRepository": true,
	"Presence": true, "DockLocker": true, "Owners": true, "Relay": true, "Kits": true,
}

// systemWide: "<domain>.<Port>.<Method>" → why it spans organizations.
var systemWide = map[string]string{
	"dock.Commands.PairingChanged":    "gateway callback about a dock the gateway runs (keyed by dock id)",
	"dock.Commands.SetLinked":         "gateway callback about a dock the gateway runs (keyed by dock id)",
	"dock.Commands.SetStatus":         "gateway callback about a dock the gateway runs (keyed by dock id)",
	"dock.Repository.Get":             "gateway callbacks and owner reconciliation resolve a dock (and its organization) by id",
	"dock.Owners.Renew":               "a process renews its own leases (keyed by process)",
	"dock.Owners.Claim":               "a process takes ownerless docks of every organization",
	"dock.Owners.ReleaseAll":          "a stopping process gives up its own leases (keyed by process)",
	"dock.Relay.Serve":                "a process serves the requests addressed to it, of every organization",
	"dock.Repository.SetStatus":       "gateway callback (dock id from the gateway's own session)",
	"dock.Repository.SetLinked":       "gateway callback (dock id from the gateway's own session)",
	"crew.TokenCommands.Authenticate": "a crew token value names its organization (agent sign-in)",
	"crew.TokenRepository.FindByHash": "a crew token value names its organization (agent sign-in)",
	"crew.TokenRepository.Touch":      "records a use of the token just authenticated (keyed by its id)",
	"ship.Commands.ReleaseExpired":    "lease sweeper requeues every organization",
	"ship.Repository.ReleaseExpired":  "lease sweeper requeues every organization",
	"crew.Presence.Touch":             "a process keeps its own connections alive (keyed by process)",
	"crew.Presence.Reap":              "any process reaps a dead process's connections, of every organization",
	"crew.Presence.Signal":            "a cross-process signal; the signal names its organization",
	"crew.Presence.Listen":            "a process listens to the signals of every organization",
	"hosting.Repository.Supervise":    "the slot supervisor wakes, stops and reconciles the slots of every organization",
	"hosting.Repository.PortID":       "the deployment's own id (labels its containers), not a tenant's",
}

func tenantViolations(files []goFile) []string {
	var out []string
	for _, f := range files {
		dir := path.Dir(f.rel)
		if !tenantDomains[dir] || path.Base(f.rel) != "ports.go" {
			continue
		}
		for _, decl := range f.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !tenantPorts[ts.Name.Name] {
					continue
				}
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				for _, m := range it.Methods.List {
					fn, ok := m.Type.(*ast.FuncType)
					if !ok || len(m.Names) == 0 {
						continue // embedded interface
					}
					key := dir + "." + ts.Name.Name + "." + m.Names[0].Name
					if _, ok := systemWide[key]; ok {
						continue
					}
					if !organizationSecond(fn) {
						out = append(out, f.fset.Position(m.Pos()).String()+": "+key+
							" must take (ctx, organization identity.OrganizationID, ...) or be listed in systemWide")
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// organizationSecond: params are (ctx context.Context, organization
// identity.OrganizationID, ...).
func organizationSecond(fn *ast.FuncType) bool {
	var params []ast.Expr
	var names []string
	for _, p := range fn.Params.List {
		if len(p.Names) == 0 {
			params, names = append(params, p.Type), append(names, "")
			continue
		}
		for _, n := range p.Names {
			params, names = append(params, p.Type), append(names, n.Name)
		}
	}
	return len(params) >= 2 && isSelector(params[0], "context", "Context") &&
		isSelector(params[1], "identity", "OrganizationID") && names[1] == "organization"
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

func TestTenantFirstPorts(t *testing.T) { report(t, tenantViolations(sources(t, false))) }

func TestTenantRuleCatchesViolations(t *testing.T) {
	files := parseFixtures(t, map[string]string{
		"dock/ports.go": `package dock
type Commands interface {
	Create(ctx context.Context, organization identity.OrganizationID, in Create) (Dock, error)
	Rename(ctx context.Context, dock identity.DockID, name string) error
	PairingChanged(ctx context.Context, dock identity.DockID)
}
type Queries interface {
	Find(ctx context.Context, org identity.OrganizationID, dock identity.DockID) (Dock, error)
}
type Gateway interface { Link(ctx context.Context, d Dock) error }`,
		"connector/ports.go": `package connector
type Commands interface { Hello(ctx context.Context) error }`,
		"crew/ports.go": `package crew
type TokenQueries interface { List(ctx context.Context) error }
type Disconnector interface { Disconnect(organization identity.OrganizationID) }`,
	})
	expectViolations(t, tenantViolations(files),
		"dock/ports.go: dock.Commands.Rename must take",
		"dock/ports.go: dock.Queries.Find must take", // named org, not organization
		"crew/ports.go: crew.TokenQueries.List must take",
	)
}
