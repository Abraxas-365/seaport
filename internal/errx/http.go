package errx

// GenericMessage is the only message a 5xx ever shows to a client; the cause
// is logged next to the request id.
const GenericMessage = "an unexpected error occurred"

// Response is the wire shape of every error body: {"error": Response}.
type Response struct {
	Type       string         `json:"type"`
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	StatusCode int            `json:"status_code"`
	Details    map[string]any `json:"details,omitempty"`
	RequestID  string         `json:"request_id"`
}

func (e *Error) ToResponse(requestID string) Response {
	r := Response{Type: string(e.Type), Code: e.Code, Message: e.Message,
		StatusCode: e.HTTPStatus, Details: e.Details, RequestID: requestID}
	if e.HTTPStatus >= 500 {
		r.Message, r.Details = GenericMessage, nil
	}
	return r
}

// FromStatus maps a transport status (router 404/405, body limit 413...) to
// a typed error so it goes through the same error body.
func FromStatus(status int, message string) *Error {
	switch status {
	case 400:
		return Validation(message)
	case 401:
		return Unauthorized(message)
	case 403:
		return Forbidden(message)
	case 404:
		return NotFound(message)
	case 405:
		return MethodNotAllowed(message)
	case 409:
		return Conflict(message)
	case 413:
		return PayloadTooLarge(message)
	case 415:
		return UnsupportedMediaType(message)
	case 426:
		return UpgradeRequired(message)
	case 422:
		return Business(message)
	case 429:
		return TooManyRequests(message)
	}
	if status >= 500 {
		return Internal(GenericMessage)
	}
	return Validation(message)
}
