package config

import (
	"strings"
	"testing"
)

func iamServer() Server {
	return Server{
		DatabaseURL: "postgres://x", Auth: AuthIAM, PublicURL: "https://seaport.example.com",
		SecretKey: "kq7Vt2xLm9Pz4Rw8Ys1Bn6Hc3Jd5Fg0A",
		IAMKit: IAMKit{URL: "https://iam.example.com", PublicURL: "https://iam.example.com", Environment: "e",
			Application: "a", Resource: "r", Audience: "https://seaport.example.com", WebClientID: "c"},
	}
}

func TestIAMURLsNeedHTTPS(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*Server)
		bad  string // "" = valid
	}{
		"https everywhere": {func(*Server) {}, ""},
		"local http": {func(c *Server) {
			c.PublicURL, c.IAMKit.URL, c.IAMKit.PublicURL = "http://127.0.0.1:8740", "http://localhost:8080", "http://[::1]:8080"
		}, ""},
		"iamkit on a docker network": {func(c *Server) { c.IAMKit.URL = "http://iamkit:8080" }, ""},
		"iamkit on a private ip":     {func(c *Server) { c.IAMKit.URL = "http://10.0.0.5:8080" }, ""},
		"public http app":            {func(c *Server) { c.PublicURL = "http://seaport.example.com" }, "SEAPORT_PUBLIC_URL"},
		"public http iamkit":         {func(c *Server) { c.IAMKit.URL = "http://iam.example.com" }, "IAMKIT_URL"},
		"browser iamkit by name":     {func(c *Server) { c.IAMKit.PublicURL = "http://iamkit:8080" }, "IAMKIT_PUBLIC_URL"},
		"not a url":                  {func(c *Server) { c.IAMKit.URL = "iam.example.com" }, "IAMKIT_URL"},
		"short secret key":           {func(c *Server) { c.SecretKey = "short" }, "SEAPORT_SECRET_KEY"},
		"no secret key":              {func(c *Server) { c.SecretKey = "" }, "SEAPORT_SECRET_KEY"},
		"placeholder secret key":     {func(c *Server) { c.SecretKey = strings.Repeat("changeme", 4) }, "SEAPORT_SECRET_KEY"},
		"token settings":             {func(c *Server) { c.AdminToken = strings.Repeat("a", 16) }, "SEAPORT_ADMIN_TOKEN"},
	} {
		t.Run(name, func(t *testing.T) {
			c := iamServer()
			tc.edit(&c)
			err := c.Validate()
			switch {
			case tc.bad == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
				t.Fatalf("want an error about %s, got %v", tc.bad, err)
			}
		})
	}
}

func TestCheckSecretKey(t *testing.T) {
	eleven := strings.Repeat("abcdefghijk", 3) // 33 chars, 11 distinct
	twelve := strings.Repeat("abcdefghijkl", 3)
	for key, ok := range map[string]bool{
		"":                                 false,
		strings.Repeat("x", 31) + "y":      false,
		eleven:                             false,
		twelve:                             true,
		"kq7Vt2xLm9Pz4Rw8Ys1Bn6Hc3Jd5Fg0A": true,
	} {
		if err := CheckSecretKey(key); (err == nil) != ok {
			t.Errorf("CheckSecretKey(%q) = %v, want ok=%v", key, err, ok)
		}
	}
}
