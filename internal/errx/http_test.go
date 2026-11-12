package errx

import "testing"

func TestFromStatusKeepsTheTransportStatus(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 405, 409, 413, 415, 422, 426, 429} {
		if got := FromStatus(status, "x").HTTPStatus; got != status {
			t.Errorf("FromStatus(%d) answers %d", status, got)
		}
	}
	if got := FromStatus(502, "x"); got.HTTPStatus != 500 || got.Message != GenericMessage {
		t.Errorf("5xx: %d %q", got.HTTPStatus, got.Message)
	}
}
