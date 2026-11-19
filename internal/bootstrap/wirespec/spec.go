// Package wirespec lists the Go types the web reads from the API (the input
// of internal/bootstrap/wiregen, which renders web/src/lib/types.gen.ts) and
// is what the wire fixtures test checks the fixtures against. It lives under
// bootstrap because it names types of every domain and adapter.
package wirespec

import (
	"reflect"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/agent/adapters/agenthttp"
	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockdatadog"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockemail"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockgithub"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockjira"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockslack"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockwebhook"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/vault"
	"github.com/Abraxas-365/seaport/internal/vault/adapters/vaulthttp"

	"github.com/Abraxas-365/seaport/internal/wire"
)

const module = "github.com/Abraxas-365/seaport"

func of[T any]() reflect.Type { return reflect.TypeFor[T]() }

func field[T any](name string) wire.Field { return wire.Field{Type: of[T](), Name: name} }

// Spec is everything the web reads from the API: the response types (the
// TypeScript name on the left is the one the web imports), the enum-like
// strings and the payload of every live event.
//
// A new response type, or a type a field of one starts to use, goes into
// Types; a new constant goes into the const block its enum names (it needs
// no entry here); a new enum-like string type or field goes into Enums
// (plus FieldEnums for a plain string field). The renderer refuses a type
// it can't name, so forgetting one fails `go generate`, never silently
// becomes `string`. See "Wire types" in AGENTS.md.
func Spec() wire.Spec {
	return wire.Spec{
		Module:    module,
		Generator: "internal/bootstrap/wiregen",
		Preamble:  []string{`import type { Permission } from "./permissions.gen"`},
		Types: map[string]reflect.Type{
			// docks
			"Dock":        of[dock.Dock](),
			"DockTools":   of[dock.Tools](),
			"Destination": of[dock.Destination](),
			"DockDeleted": of[dock.Deleted](),
			// per-channel dock settings (GET /docks/{id}/<channel>)
			"GitHubApp":          of[dockgithub.AppInfo](),
			"GitHubInstallation": of[dockgithub.Installation](),
			"GitHubInfo":         of[dockgithub.Info](),
			"GitHubManifest":     of[dockgithub.Manifest](),
			"JiraSite":           of[dockjira.SiteInfo](),
			"JiraInfo":           of[dockjira.Info](),
			"SlackApp":           of[dockslack.AppInfo](),
			"SlackInfo":          of[dockslack.Info](),
			"WebhookTemplate":    of[dockwebhook.Template](),
			"WebhookPreview":     of[dockwebhook.Message](),
			"WebhookInfo":        of[dockwebhook.Info](),
			"DatadogMirror":      of[dockdatadog.Mirror](),
			"DatadogInfo":        of[dockdatadog.Info](),
			"EmailEndpoint":      of[dockemail.Endpoint](),
			"EmailAccount":       of[dockemail.AccountInfo](),
			"EmailInfo":          of[dockemail.Info](),
			"EmailLink":          of[dockemail.LinkRequest](),
			// conversations and customs
			"Conversation": of[conversation.Conversation](),
			"Policy":       of[conversation.Policy](),
			"Rule":         of[conversation.Rule](),
			"Customs":      of[conversation.Customs](),
			// ships
			"Ship":          of[ship.Ship](),
			"Attachment":    of[ship.Attachment](),
			"InboxItem":     of[ship.InboxItem](),
			"Delivery":      of[ship.Delivery](),
			"DeliveryEvent": of[ship.DeliveryEvent](),
			"Approval":      of[ship.Approval](),
			"Turn":          of[ship.Turn](),
			"TurnTool":      of[ship.TurnTool](),
			// crew
			"CrewStatus":      of[crew.Status](),
			"Agent":           of[crew.Agent](),
			"AgentDelivery":   of[crew.AgentDelivery](),
			"ActivityEvent":   of[crew.Activity](),
			"ActivityItem":    of[crew.ActivityItem](),
			"CrewToken":       of[crew.Token](),
			"IssuedCrewToken": of[crew.Issued](),
			// the agent library
			"LibraryAgent":   of[agent.Agent](),
			"CloneAgent":     of[agenthttp.CloneRequest](),
			"AgentDeleted":   of[agent.Deleted](),
			"Assignment":     of[agent.Assignment](),
			"TeamMember":     of[agent.Member](),
			"AgentProfile":   of[agent.Profile](),
			"ProfileVersion": of[agent.Version](),
			"Skill":          of[agent.Skill](),
			"McpServer":      of[agent.MCPServer](),
			"McpHeader":      of[agent.Header](),
			"AgentSecret":    of[agent.Secret](),
			"Takeover":       of[agent.Takeover](),
			"TakeoverEvent":  of[agent.TakeoverEvent](),
			// schedules
			"Schedule": of[schedule.Schedule](),
			// vaults
			"Vault":             of[vault.Vault](),
			"VaultDetail":       of[vaulthttp.Detail](),
			"VaultAgentGrant":   of[vault.AgentGrant](),
			"VaultDockGrant":    of[vault.DockGrant](),
			"VaultAccess":       of[vault.Access](),
			"VaultEntry":        of[vault.Entry](),
			"VaultEntrySummary": of[vault.Summary](),
			"VaultEntryVersion": of[vault.Version](),
			"VaultHit":          of[vault.Hit](),
			"VaultEntryChange":  of[vault.EntryChange](),
			"Provenance":        of[vault.Provenance](),
			// hosting (an agent config's machine: agent.Profile.Machine)
			"Machine":          of[hosting.Machine](),
			"MachineRepo":      of[hosting.Repo](),
			"MachineModels":    of[hosting.Models](),
			"MachineNetwork":   of[hosting.Network](),
			"MachineBrowser":   of[hosting.Browser](),
			"MachineResources": of[hosting.Resources](),
			"Build":            of[hosting.Build](),
			"Hosted":           of[hosting.Hosted](),
			"Slot":             of[hosting.Slot](),
			"SlotLog":          of[hosting.SlotLog](),
			"SlotLogin":        of[hosting.Login](),
			"SlotAccount":      of[hosting.Account](),
			"SlotBrowserEvent": of[hosting.BrowserEvent](),
			"Home":             of[hosting.Home](),
			"HomeFile":         of[hosting.HomeFile](),
			"HomeRules":        of[hosting.HomeRules](),
			"SecretPattern":    of[hosting.SecretPattern](),
			"Denial":           of[hosting.Denial](),
			"UsageDay":         of[hosting.UsageDay](),
			// members (iam mode)
			"MemberAccess":       of[auth.Access](),
			"Member":             of[auth.Member](),
			"Invitation":         of[auth.Invitation](),
			"MemberApiKey":       of[auth.APIKey](),
			"IssuedMemberApiKey": of[auth.IssuedAPIKey](),
			// the rest
			"Page":    of[query.Page](),
			"Session": of[httpx.SessionState](),
		},
		Enums: []wire.Enum{
			{Name: "Channel", Type: of[dock.Channel](), Anchor: "ChannelWhatsApp"},
			{Name: "DockStatus", Type: of[dock.Status](), Anchor: "StatusLinking"},
			{Name: "ConversationKind", Type: of[conversation.Kind](), Anchor: "KindDirect"},
			{Name: "Access", Type: of[conversation.Access](), Anchor: "AccessAuto"},
			{Name: "GroupMode", Type: of[conversation.GroupMode](), Anchor: "GroupMentions"},
			{Name: "CustomsStatus", Type: of[conversation.Status](), Anchor: "StatusAllowed"},
			{Name: "DefaultAccess", Type: of[conversation.DefaultAccess](), Anchor: "DefaultAsk"},
			{Name: "Effect", Type: of[conversation.Effect](), Anchor: "EffectAllow"},
			{Name: "Direction", Type: of[ship.Direction](), Anchor: "DirectionIn"},
			{Name: "ShipStatus", Type: of[ship.Status](), Anchor: "StatusHeld"},
			{Name: "AttachmentKind", Type: of[ship.AttachmentKind](), Anchor: "AttachmentImage"},
			{Name: "ApprovalStatus", Type: of[ship.ApprovalStatus](), Anchor: "ApprovalPending"},
			{Name: "TurnOutcome", Type: of[ship.TurnOutcome](), Anchor: "TurnAcked"},
			{Name: "DeliveryState", Type: of[ship.DeliveryState](), Anchor: "DeliveryClaimed"},
			{Name: "TakeoverKind", Type: of[agent.TakeoverKind](), Anchor: "KindLogin"},
			{Name: "TakeoverBy", Type: of[agent.TakeoverBy](), Anchor: "TakeoverByAgent"},
			{Name: "TakeoverState", Type: of[agent.TakeoverState](), Anchor: "TakeoverStateAsked"},
			{Name: "VaultMode", Type: of[vault.Mode](), Anchor: "ModeRead"},
			{Name: "ProvenanceKind", Type: of[vault.ProvenanceKind](), Anchor: "WrittenByAgent"},
			// string fields in Go: the block of constants says the values
			{Name: "ActivityKind", Pkg: "crew", Anchor: "ActivityThought"},
			{Name: "ToolStatus", Pkg: "crew", Anchor: "ToolPending"},
			{Name: "Approve", Pkg: "agent", Anchor: "ApproveAllow", Also: []string{""}},
			{Name: "EmailTLS", Pkg: "dock/adapters/dockemail", Anchor: "TLS"},
			{Name: "Harness", Pkg: "hosting", Anchor: "HarnessRness"},
			{Name: "RepoConfig", Pkg: "hosting", Anchor: "RepoConfigTrust"},
			{Name: "WorkspaceKind", Pkg: "hosting", Anchor: "WorkspacePersistent"},
			{Name: "ResourceSize", Pkg: "hosting", Anchor: "SizeS"},
			{Name: "BrowserEngine", Pkg: "hosting", Anchor: "BrowserChromium", Also: []string{""}},
			{Name: "BrowserState", Pkg: "hosting", Anchor: "BrowserOpened"},
			{Name: "BuildState", Pkg: "hosting", Anchor: "BuildBuilding"},
			{Name: "SlotState", Pkg: "hosting", Anchor: "SlotStopped"},
			{Name: "LoginState", Pkg: "hosting", Anchor: "LoginStarting"},
		},
		FieldEnums: map[wire.Field]string{
			field[crew.ActivityItem]("Kind"):     "ActivityKind",
			field[ship.TurnTool]("Status"):       "ToolStatus",
			field[agent.Profile]("Approve"):      "Approve",
			field[dockemail.Endpoint]("TLS"):     "EmailTLS",
			field[hosting.Home]("Harness"):       "Harness",
			field[hosting.Machine]("RepoConfig"): "RepoConfig",
			field[hosting.Machine]("Workspace"):  "WorkspaceKind",
			field[hosting.Resources]("Size"):     "ResourceSize",
			field[hosting.Browser]("Engine"):     "BrowserEngine",
			field[hosting.BrowserEvent]("State"): "BrowserState",
			field[hosting.Build]("State"):        "BuildState",
			field[hosting.Slot]("State"):         "SlotState",
			field[hosting.Login]("State"):        "LoginState",
		},
		Raw: map[wire.Field]string{
			// "" in an update: the call's status didn't change
			field[crew.ActivityItem]("Status"): `ToolStatus | ""`,
			// a policy always has a mode (the "" of a conversation means "the dock's")
			field[conversation.Policy]("GroupMode"): `Exclude<GroupMode, "">`,
			// permission names (permissions.gen.ts)
			field[httpx.SessionState]("Permissions"): "Permission[]",
			// a config's harness may be unset (a remote agent's), and its
			// machine is hosting's (opaque to agent: json.RawMessage)
			field[agent.Profile]("Harness"): `Harness | ""`,
			field[agent.Profile]("Machine"): "Machine | null",
		},
		Events: map[string]reflect.Type{
			dock.EventUpdated:         of[dock.Dock](),
			dock.EventDeleted:         of[dock.Deleted](),
			conversation.EventUpdated: of[conversation.Conversation](),
			conversation.EventCustoms: of[conversation.Customs](),
			ship.EventCreated:         of[ship.Ship](),
			ship.EventUpdated:         of[ship.Ship](),
			ship.EventDelivery:        of[ship.DeliveryEvent](),
			ship.EventTurn:            of[ship.Turn](),
			ship.EventApproval:        of[ship.Approval](),
			crew.EventChanged:         of[crew.Status](),
			crew.EventActivity:        of[crew.Activity](),
			agent.EventUpdated:        of[agent.Agent](),
			agent.EventDeleted:        of[agent.Deleted](),
			agent.EventTakeover:       of[agent.TakeoverEvent](),

			hosting.EventSlotsUpdated: of[hosting.Hosted](),
			hosting.EventAgentBuild:   of[hosting.Build](),
			hosting.EventSlotBrowser:  of[hosting.BrowserEvent](),
			schedule.EventCreated:     of[schedule.Schedule](),
			schedule.EventUpdated:     of[schedule.Schedule](),
			schedule.EventDeleted:     of[schedule.Schedule](),
			schedule.EventFired:       of[schedule.Schedule](),
			vault.EventCreated:        of[vault.Vault](),
			vault.EventUpdated:        of[vault.Vault](),
			vault.EventDeleted:        of[vault.Vault](),
			vault.EventEntry:          of[vault.EntryChange](),
		},
	}
}
