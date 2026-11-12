package httpx

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
)

// SessionCookie carries a browser session of the admin.
const SessionCookie = "seaport_session"

// Sessions lets a browser trade the admin token for an HttpOnly cookie
// (EventSource and plain links can't send headers, and the token must not
// live in page JavaScript). The cookie is stateless — "<expiry>.<hmac>"
// keyed by the admin token — so rotating the token ends every session.
// Cookie-authenticated unsafe requests must pass CSRF protection.
type Sessions struct {
	Token string
	// Secure marks the cookie HTTPS-only (public URL is https).
	Secure bool
	TTL    time.Duration
}

func (s Sessions) sign(expiry int64) string {
	m := hmac.New(sha256.New, []byte(s.Token))
	m.Write([]byte("seaport-session:" + strconv.FormatInt(expiry, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// valid reports whether the request carries a live session cookie, and
// when it expires.
func (s Sessions) valid(c fiber.Ctx) (time.Time, bool) {
	value := c.Cookies(SessionCookie)
	if value == "" {
		return time.Time{}, false
	}
	rawExp, mac, ok := strings.Cut(value, ".")
	exp, err := strconv.ParseInt(rawExp, 10, 64)
	if !ok || err != nil || time.Now().Unix() >= exp {
		return time.Time{}, false
	}
	if !hmac.Equal([]byte(mac), []byte(s.sign(exp))) {
		return time.Time{}, false
	}
	return time.Unix(exp, 0).UTC(), true
}

func (s Sessions) cookie(value string, maxAge int) *fiber.Cookie {
	return &fiber.Cookie{Name: SessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HTTPOnly: true, Secure: s.Secure, SameSite: fiber.CookieSameSiteStrictMode}
}

type loginRequest struct {
	Token string `json:"token"`
}

// SessionState is GET /api/v1/session in every mode: whether this browser
// is signed in, as whom, and what it may do (the UI hides what it can't).
type SessionState struct {
	Authenticated    bool       `json:"authenticated"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Subject          string     `json:"subject,omitempty"`
	Name             string     `json:"name,omitempty"`
	Email            string     `json:"email,omitempty"`
	Organization     string     `json:"organization_id,omitempty"`
	OrganizationName string     `json:"organization_name,omitempty"`
	Permissions      []string   `json:"permissions"`
}

// SignedOut is the state of a browser without a session.
func SignedOut() SessionState { return SessionState{Permissions: []string{}} }

// Login is POST /api/v1/session {"token": admin token}: sets the cookie.
func (s Sessions) Login() fiber.Handler {
	return func(c fiber.Ctx) error {
		var req loginRequest
		if err := DecodeJSON(c, &req); err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Token)), []byte(s.Token)) != 1 {
			return errx.Unauthorized("invalid token")
		}
		exp := time.Now().Add(s.TTL).Unix()
		c.Cookie(s.cookie(strconv.FormatInt(exp, 10)+"."+s.sign(exp), int(s.TTL.Seconds())))
		at := time.Unix(exp, 0).UTC()
		return JSON(c, http.StatusOK, SessionState{Authenticated: true, ExpiresAt: &at, Subject: "admin", Permissions: permission.All})
	}
}

// Logout is DELETE /api/v1/session: clears the cookie (this browser only;
// rotate the admin token to end every session).
func (s Sessions) Logout() fiber.Handler {
	return func(c fiber.Ctx) error {
		c.Cookie(s.cookie("", -1))
		c.Status(http.StatusNoContent)
		return nil
	}
}

// Current is GET /api/v1/session: whether this browser is logged in (never
// 401, so the UI can ask before showing the login form).
func (s Sessions) Current() fiber.Handler {
	return func(c fiber.Ctx) error {
		if exp, ok := s.valid(c); ok {
			return JSON(c, http.StatusOK, SessionState{Authenticated: true, ExpiresAt: &exp, Subject: "admin", Permissions: permission.All})
		}
		return JSON(c, http.StatusOK, SignedOut())
	}
}

// RequireAdmin accepts the admin bearer token or a session cookie, and
// makes p the principal. Unsafe cookie requests from another origin are
// rejected (CSRF), and a bearer token wins over a cookie when both are sent.
func (s Sessions) RequireAdmin(p Principal) fiber.Handler {
	cookie := CrossOrigin()
	return func(c fiber.Ctx) error {
		if c.Get("Authorization") != "" {
			if err := CheckBearer(c, s.Token); err != nil {
				return err
			}
			SetPrincipal(c, p)
			return c.Next()
		}
		if _, ok := s.valid(c); ok {
			SetPrincipal(c, p)
			return cookie(c)
		}
		return errx.Unauthorized("missing or invalid token")
	}
}
