package errx

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Error is the one error type that should reach a transport boundary
// (HTTP handler, MCP tool, CLI exit). Construct it only through this
// package's functions (New, Wrap, or the semantic helpers in common.go) so
// Code/Type/HTTPStatus always stay consistent with each other.
type Error struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Type       Type           `json:"type"`
	HTTPStatus int            `json:"http_status"`
	Details    map[string]any `json:"details,omitempty"`
	// Op is the operation that wrapped an already-typed error. Logs only:
	// the client keeps seeing the original Message.
	Op  string `json:"-"`
	Err error  `json:"-"`
}

func (e *Error) Error() string {
	msg := e.Message
	if e.Op != "" {
		msg = e.Op + ": " + msg
	}
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, msg, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, msg)
}

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[key] = value
	return e
}

// MarshalJSON implements json.Marshaler.
func (e *Error) MarshalJSON() ([]byte, error) {
	type Alias Error
	return json.Marshal((*Alias)(e))
}

func New(message string, errType Type) *Error {
	return &Error{
		Code:       string(errType),
		Message:    message,
		Type:       errType,
		HTTPStatus: typeToHTTPStatus(errType),
	}
}

// Wrap wraps an existing error with context. If err is already an *Error,
// its Code/Type/HTTPStatus/Message/Details are preserved and message is kept
// as Op (logs only), so wrapping a NotFound never turns it into an Internal
// or leaks "load widget" to the client.
//
// It returns error (not *Error) so `return errx.Wrap(err, ...)` with a nil
// err is a true nil, never a typed nil.
func Wrap(err error, message string, errType Type) error {
	if err == nil {
		return nil
	}
	var existing *Error
	if errors.As(err, &existing) {
		return &Error{
			Code:       existing.Code,
			Message:    existing.Message,
			Type:       existing.Type,
			HTTPStatus: existing.HTTPStatus,
			Details:    existing.Details,
			Op:         message,
			Err:        err,
		}
	}
	return &Error{
		Code:       string(errType),
		Message:    message,
		Type:       errType,
		HTTPStatus: typeToHTTPStatus(errType),
		Err:        err,
	}
}

func Wrapf(err error, errType Type, format string, args ...any) error {
	return Wrap(err, fmt.Sprintf(format, args...), errType)
}

func Is(err, target error) bool     { return errors.Is(err, target) }
func As(err error, target any) bool { return errors.As(err, target) }

// IsType reports whether err is an *Error of the given type.
func IsType(err error, t Type) bool {
	var e *Error
	return errors.As(err, &e) && e.Type == t
}

func typeToHTTPStatus(t Type) int {
	switch t {
	case TypeValidation:
		return 400
	case TypeAuthorization:
		return 401
	case TypeNotFound:
		return 404
	case TypeConflict:
		return 409
	case TypeBusiness:
		return 422
	case TypeExternal:
		return 502
	case TypeInternal:
		return 500
	default:
		return 500
	}
}
