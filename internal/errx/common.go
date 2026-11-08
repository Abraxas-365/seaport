package errx

// Common error constructors. Prefer these over calling New directly: call
// sites read as intent ("this input is invalid"), not as a status lookup.

func Internal(message string) *Error   { return New(message, TypeInternal) }
func Validation(message string) *Error { return New(message, TypeValidation) }
func NotFound(message string) *Error   { return New(message, TypeNotFound) }

// InvalidField is a validation error about one input field (details.field).
func InvalidField(field, message string) *Error {
	return Validation(message).WithDetail("field", field)
}

// Unauthorized: the caller has no valid credentials (or none at all).
func Unauthorized(message string) *Error { return New(message, TypeAuthorization) }

// Forbidden: the caller IS authenticated but lacks permission for this
// action. Kept distinct from Unauthorized so clients can tell "log in
// again" apart from "you don't have access".
func Forbidden(message string) *Error {
	return withStatus(TypeAuthorization, "FORBIDDEN", 403, message)
}

func Conflict(message string) *Error { return New(message, TypeConflict) }
func Business(message string) *Error { return New(message, TypeBusiness) }
func External(message string) *Error { return New(message, TypeExternal) }

// TooManyRequests: a rate limit refused the action; details may carry
// retry_after_seconds (HTTP adds Retry-After).
func TooManyRequests(message string) *Error {
	return withStatus(TypeBusiness, "RATE_LIMITED", 429, message)
}

// QuotaExceeded: an organization reached a count limit (docks, crew
// tokens, live agents); freeing one (delete, revoke, disconnect) makes
// room. details carry quota and limit.
func QuotaExceeded(quota string, limit int, message string) *Error {
	return withStatus(TypeBusiness, "QUOTA_EXCEEDED", 422, message).
		WithDetail("quota", quota).WithDetail("limit", limit)
}

func MethodNotAllowed(message string) *Error {
	return withStatus(TypeValidation, "METHOD_NOT_ALLOWED", 405, message)
}

func PayloadTooLarge(message string) *Error {
	return withStatus(TypeValidation, "PAYLOAD_TOO_LARGE", 413, message)
}

func UnsupportedMediaType(message string) *Error {
	return withStatus(TypeValidation, "UNSUPPORTED_MEDIA_TYPE", 415, message)
}

func withStatus(t Type, code string, status int, message string) *Error {
	err := New(message, t)
	err.Code = code
	err.HTTPStatus = status
	return err
}
