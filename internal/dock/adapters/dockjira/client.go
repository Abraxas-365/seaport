package dockjira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
)

// client speaks Jira Cloud's REST API v3 (no SDK: basic auth with the
// service account's email and API token, three endpoints).
type client struct {
	http *http.Client
	// allow checks a site against the allow-list as it is now; every
	// request that carries credentials goes through it, so a host the
	// operator has since removed (or a stored row that never passed) gets
	// none.
	allow func(site string) error
}

func newClient(allow func(site string) error) *client {
	return &client{allow: allow, http: &http.Client{
		Timeout: 30 * time.Second,
		// Never follow a redirect: the credentials go to the site that was
		// checked against the allow-list, and to no other.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// apiError is Jira's refusal: its status and message.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("jira answered %d: %s", e.Status, e.Message) }

// refusedCredentials: Jira rejected the account (a revoked or expired
// API token, a deactivated user).
func refusedCredentials(err error) bool {
	e, ok := err.(*apiError)
	return ok && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden)
}

const maxAnswer = 1 << 20

// do calls the site's API and decodes a JSON answer into out (nil:
// ignored).
func (c *client) do(ctx context.Context, base, email, token, method, path string, body, out any) error {
	if c.allow != nil {
		if err := c.allow(base); err != nil {
			return err
		}
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return errx.Wrap(err, "encode jira request", errx.TypeInternal)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return errx.Wrap(err, "build jira request", errx.TypeInternal)
	}
	req.SetBasicAuth(email, token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errx.Wrap(err, "call jira", errx.TypeExternal)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxAnswer))
	if err != nil {
		return errx.Wrap(err, "read jira answer", errx.TypeExternal)
	}
	if res.StatusCode >= 300 {
		return &apiError{Status: res.StatusCode, Message: failure(res.StatusCode, raw)}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return errx.Wrap(err, "decode jira answer", errx.TypeExternal)
		}
	}
	return nil
}

// failure is the message of an error answer (Jira's errorMessages and
// errors, else the status text).
func failure(status int, raw []byte) string {
	var e struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		Message       string            `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil {
		parts := append([]string(nil), e.ErrorMessages...)
		for k, v := range e.Errors {
			parts = append(parts, k+": "+v)
		}
		if e.Message != "" {
			parts = append(parts, e.Message)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return http.StatusText(status)
}

// wrapped turns a Jira failure into the error callers show: external
// (502) with Jira's message; credentials refused say so.
func wrapped(err error, what string) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*apiError); ok {
		return errx.External(what + ": " + e.Error())
	}
	return err
}

// myself checks the credentials and returns the account they belong to.
func (c *client) myself(ctx context.Context, base, email, token string) (accountID, displayName string, err error) {
	var out struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		Active      *bool  `json:"active"`
	}
	if err := c.do(ctx, base, email, token, http.MethodGet, "/rest/api/3/myself", nil, &out); err != nil {
		return "", "", err
	}
	if out.AccountID == "" {
		return "", "", errx.External("jira did not say which account these credentials belong to (is this a Jira Cloud site?)")
	}
	if out.Active != nil && !*out.Active {
		return "", "", &apiError{Status: http.StatusForbidden, Message: "the account is deactivated"}
	}
	return out.AccountID, out.DisplayName, nil
}

// propertyKey is the comment property Send stamps its comments with.
const propertyKey = "seaport"

// comment posts an ADF comment on an issue, stamped with the Seaport
// message id (a comment property), and returns the comment's id.
func (c *client) comment(ctx context.Context, s Site, key string, body doc, messageID string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	req := map[string]any{"body": body,
		"properties": []map[string]any{{"key": propertyKey, "value": map[string]string{"message": messageID}}}}
	err := c.do(ctx, s.URL, s.Email, s.token, http.MethodPost, "/rest/api/3/issue/"+url.PathEscape(key)+"/comment", req, &out)
	return out.ID, err
}

// stamp reads the Seaport message id a comment was stamped with ("" when
// it has none: posted some other way).
func (c *client) stamp(ctx context.Context, s Site, commentID string) (string, error) {
	var out struct {
		Value struct {
			Message string `json:"message"`
		} `json:"value"`
	}
	err := c.do(ctx, s.URL, s.Email, s.token, http.MethodGet,
		"/rest/api/3/comment/"+url.PathEscape(commentID)+"/properties/"+propertyKey, nil, &out)
	if e, ok := err.(*apiError); ok && e.Status == http.StatusNotFound {
		return "", nil
	}
	return out.Value.Message, err
}
