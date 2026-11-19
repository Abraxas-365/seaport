package bootstrap

import (
	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/vault"
)

// EventPermissions is the read permission an admin needs to receive each
// live event type on /api/v1/events (the same one that guards the route
// returning that data). A type missing here is never streamed;
// TestEveryEventTypeHasAPermission keeps it complete.
var EventPermissions = map[string]string{
	dock.EventUpdated:         permission.DocksRead,
	dock.EventDeleted:         permission.DocksRead,
	conversation.EventUpdated: permission.ConversationsRead,
	conversation.EventCustoms: permission.CustomsRead,
	ship.EventCreated:         permission.ShipsRead,
	ship.EventUpdated:         permission.ShipsRead,
	ship.EventDelivery:        permission.ShipsRead,
	ship.EventTurn:            permission.ShipsRead,
	ship.EventApproval:        permission.ShipsRead,
	crew.EventChanged:         permission.CrewRead,
	// Activity shows what the agent reads and writes in a conversation:
	// the same permission as its messages.
	crew.EventActivity: permission.ShipsRead,
	agent.EventUpdated: permission.AgentsRead,
	agent.EventDeleted: permission.AgentsRead,
	// The audit trail of takeovers: who asked a person into the agent's
	// browser and who answered; the agent's takeovers list is the state.
	agent.EventTakeover:       permission.AgentsRead,
	hosting.EventSlotsUpdated: permission.HostingRead,
	hosting.EventAgentBuild:   permission.HostingRead,
	hosting.EventSlotBrowser:  permission.BrowserUse,
	schedule.EventCreated:     permission.SchedulesRead,
	schedule.EventUpdated:     permission.SchedulesRead,
	schedule.EventDeleted:     permission.SchedulesRead,
	schedule.EventFired:       permission.SchedulesRead,
	vault.EventCreated:        permission.VaultsRead,
	vault.EventUpdated:        permission.VaultsRead,
	vault.EventDeleted:        permission.VaultsRead,
	vault.EventEntry:          permission.VaultsRead,
}
