package vault

import "github.com/Abraxas-365/seaport/internal/identity"

// Events the vault domain publishes (see internal/event). None carries an
// entry's body: a client that needs it reads the entry.
const (
	// EventCreated, EventUpdated (the vault's title or description, or a
	// grant added, changed or removed) and EventDeleted carry the Vault
	// (a deleted one as it was).
	EventCreated = "vault.created"
	EventUpdated = "vault.updated"
	EventDeleted = "vault.deleted"
	// EventEntry: an entry was saved, reverted or deleted; the data is an
	// EntryChange.
	EventEntry = "vault.entry"
)

// EntryChange is the data of EventEntry.
type EntryChange struct {
	Vault   identity.VaultID `json:"vault_id"`
	Entry   identity.EntryID `json:"entry_id"`
	Name    string           `json:"name"`
	Version int              `json:"version"`
	// Deleted: the entry is gone (Version is its last).
	Deleted    bool       `json:"deleted"`
	Provenance Provenance `json:"provenance"`
}
