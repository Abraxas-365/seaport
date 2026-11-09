package dock

import (
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Several processes may serve one database. A dock whose channel keeps one
// live connection per account (Gateway.Exclusive: WhatsApp) is run by one
// process at a time, its owner (Owners, by lease); the others hand it their
// work through a Relay.

// RequestKind is the work a Relay carries to a dock's owner.
type RequestKind string

const (
	RequestLink   RequestKind = "link"
	RequestUnlink RequestKind = "unlink"
	RequestSend   RequestKind = "send"
)

func (k RequestKind) Valid() bool {
	return k == RequestLink || k == RequestUnlink || k == RequestSend
}

// Request is one piece of work for a dock's owner.
type Request struct {
	Dock identity.DockID `json:"dock"`
	Kind RequestKind     `json:"kind"`
	// Outgoing is the message of a send.
	Outgoing *Outgoing `json:"outgoing,omitempty"`
}

func (r Request) Validate() error {
	if r.Dock.IsZero() {
		return errx.InvalidField("dock", "dock is required")
	}
	if !r.Kind.Valid() {
		return errx.InvalidField("kind", "kind must be link, unlink or send")
	}
	if r.Kind != RequestSend {
		return nil
	}
	if r.Outgoing == nil || r.Outgoing.Dock != r.Dock {
		return errx.InvalidField("outgoing", "a send carries a message of its dock")
	}
	return r.Outgoing.Validate()
}

// Reply is the owner's answer: the dock's status after a link, or the
// error the work ended with.
type Reply struct {
	Status Status      `json:"status,omitempty"`
	Error  *errx.Error `json:"error,omitempty"`
}

// Err is the reply's error, or nil.
func (r Reply) Err() error {
	if r.Error == nil {
		return nil
	}
	return r.Error
}

// movedCode marks Moved errors.
const movedCode = "DOCK_MOVED"

// Moved: the process asked to run a dock's work no longer owns it (its
// lease ended, or it is stopping); the caller asks the new owner.
func Moved() *errx.Error {
	err := errx.Conflict("the dock moved to another process; try again")
	err.Code = movedCode
	return err
}

// IsMoved reports a Moved error.
func IsMoved(err error) bool {
	var e *errx.Error
	return errx.As(err, &e) && e.Code == movedCode
}
