// Package sample holds the Go types internal/wire's tests render: one of
// every shape the renderer understands (and the ones it must refuse live in
// the test itself, as anonymous types).
package sample

import (
	"encoding/json"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// Color is an enum-like string.
type Color string

const (
	// Red is the first.
	Red  Color = "red"
	Blue Color = "blue"
)

// Mode is a plain string in the structs that use it.
const (
	ModeFast = "fast"
	ModeSlow = "slow"
)

// Inner is embedded in Widget.
type Inner struct {
	// Note is flattened into the embedder.
	Note string `json:"note"`
}

// Widget has one of everything.
type Widget struct {
	Inner
	ID      identity.DockID `json:"id"`
	Secret  string          `json:"-"`
	private string
	// Name is required.
	Name    string            `json:"name"`
	Color   Color             `json:"color"`
	Mode    string            `json:"mode"`
	Count   int               `json:"count,omitempty"`
	Maybe   *string           `json:"maybe"`
	Omitted *string           `json:"omitted,omitempty"`
	At      time.Time         `json:"at"`
	Seen    *time.Time        `json:"seen"`
	Tags    []string          `json:"tags"`
	Kids    []Child           `json:"kids"`
	Labels  map[string]string `json:"labels,omitempty"`
	Raw     json.RawMessage   `json:"raw"`
	Blob    []byte            `json:"blob"`
	Any     any               `json:"any"`
	Anon    struct {
		X int `json:"x"`
	} `json:"anon"`
	Default string
	IDs     []identity.ShipID `json:"ids"`
}

// Child is a named struct used by Widget.
type Child struct {
	Ok bool `json:"ok"`
}

// Marshaled has its own JSON encoding.
type Marshaled struct{ V int }

func (Marshaled) MarshalJSON() ([]byte, error) { return []byte(`1`), nil }

// Stray is a string type nobody registered.
type Stray string

// HasStray uses it.
type HasStray struct {
	S Stray `json:"s"`
}

// HasChild uses a named struct.
type HasChild struct {
	C Child `json:"c"`
}

// HasMarshaled uses a type with its own encoding.
type HasMarshaled struct {
	M Marshaled `json:"m"`
}
