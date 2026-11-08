package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

func TestRequire(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := httpx.Require(permission.DocksWrite, ok)
	cases := []struct {
		name string
		p    *httpx.Principal
		want int
	}{
		{"no principal", nil, http.StatusUnauthorized},
		{"admin without it", &httpx.Principal{Organization: org, Role: httpx.RoleAdmin, Permissions: []string{permission.DocksRead}}, http.StatusForbidden},
		{"crew with it", &httpx.Principal{Organization: org, Role: httpx.RoleCrew, Permissions: permission.All}, http.StatusForbidden},
		{"admin with it", &httpx.Principal{Organization: org, Role: httpx.RoleAdmin, Permissions: []string{permission.DocksWrite}}, http.StatusTeapot},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/docks", nil)
		if c.p != nil {
			r = r.WithContext(httpx.WithPrincipal(r.Context(), *c.p))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}
}
