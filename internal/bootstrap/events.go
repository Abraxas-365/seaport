package bootstrap

import (
	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// EventPermissions is the read permission an admin needs to receive each
// live event type on /api/v1/events (the same one that guards the route
// returning that data). A type missing here is never streamed;
// TestEveryEventTypeHasAPermission keeps it complete.
var EventPermissions = map[string]string{
	dock.EventUpdated:               permission.DocksRead,
	dock.EventDeleted:               permission.DocksRead,
	conversation.EventUpdated:       permission.ConversationsRead,
	conversation.EventCustoms:       permission.CustomsRead,
	ship.EventCreated:               permission.ShipsRead,
	ship.EventUpdated:               permission.ShipsRead,
	ship.EventDelivery:              permission.ShipsRead,
	crew.EventChanged:               permission.CrewRead,
	agent.EventUpdated:              permission.AgentsRead,
	agent.EventDeleted:              permission.AgentsRead,
	hosting.EventEnvironmentUpdated: permission.EnvironmentsRead,
	hosting.EventEnvironmentDeleted: permission.EnvironmentsRead,
	hosting.EventSlotsUpdated:       permission.EnvironmentsRead,
	hosting.EventEnvironmentBuild:   permission.EnvironmentsRead,
}
