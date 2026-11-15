package vault_test

import (
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/vault"
)

func TestResolveTakesTheWidestModePerVault(t *testing.T) {
	a, b, c := identity.NewVaultID(), identity.NewVaultID(), identity.NewVaultID()
	own := []vault.Grant{{Vault: a, Mode: vault.ModeRead}, {Vault: b, Mode: vault.ModeWrite}}
	dock := []vault.Grant{{Vault: a, Mode: vault.ModeWrite}, {Vault: b, Mode: vault.ModeRead}, {Vault: c, Mode: vault.ModeRead}}
	got := vault.Resolve(own, dock)
	want := []vault.Grant{{Vault: a, Mode: vault.ModeWrite}, {Vault: b, Mode: vault.ModeWrite}, {Vault: c, Mode: vault.ModeRead}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("grant %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// No dock: the agent's own, as they are. Nothing at all: nothing.
	if got := vault.Resolve(own, nil); len(got) != 2 || got[0].Mode != vault.ModeRead {
		t.Errorf("own only: %+v", got)
	}
	if got := vault.Resolve(nil, nil); len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
	// An unknown mode grants nothing, and the inputs are left alone.
	if got := vault.Resolve([]vault.Grant{{Vault: a, Mode: "admin"}}, nil); len(got) != 0 {
		t.Errorf("unknown mode: %+v", got)
	}
	if own[0].Mode != vault.ModeRead {
		t.Error("Resolve changed its input")
	}
}

func TestModeAllows(t *testing.T) {
	for mode, want := range map[vault.Mode][2]bool{vault.ModeRead: {true, false}, vault.ModeWrite: {true, true}, "": {false, false}} {
		if mode.Allows(false) != want[0] || mode.Allows(true) != want[1] {
			t.Errorf("mode %q: read %v write %v", mode, mode.Allows(false), mode.Allows(true))
		}
	}
	if vault.Mode("x").Validate() == nil || vault.ModeWrite.Validate() != nil {
		t.Error("Mode.Validate")
	}
}

func TestInputsValidate(t *testing.T) {
	v := identity.NewVaultID()
	prov := vault.Provenance{Kind: vault.WrittenByUser, By: "Ana"}
	ok := vault.SaveEntry{Vault: v, Name: "faq", Body: "# FAQ", Provenance: prov}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid entry refused: %v", err)
	}
	for name, mut := range map[string]func(*vault.SaveEntry){
		"no vault":      func(s *vault.SaveEntry) { s.Vault = identity.VaultID{} },
		"upper name":    func(s *vault.SaveEntry) { s.Name = "FAQ" },
		"spaced name":   func(s *vault.SaveEntry) { s.Name = "a b" },
		"long name":     func(s *vault.SaveEntry) { s.Name = strings.Repeat("a", 65) },
		"empty name":    func(s *vault.SaveEntry) { s.Name = "" },
		"path name":     func(s *vault.SaveEntry) { s.Name = "../x" },
		"no body":       func(s *vault.SaveEntry) { s.Body = "  " },
		"huge body":     func(s *vault.SaveEntry) { s.Body = strings.Repeat("x", vault.MaxEntryBody+1) },
		"control chars": func(s *vault.SaveEntry) { s.Body = "a\x00b" },
		"negative base": func(s *vault.SaveEntry) { s.Base = -1 },
		"no provenance": func(s *vault.SaveEntry) { s.Provenance = vault.Provenance{} },
	} {
		s := ok
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, c := range map[string]vault.Create{
		"bad name":      {Name: "Company"},
		"long title":    {Name: "a", Title: strings.Repeat("x", 300)},
		"long describe": {Name: "a", Description: strings.Repeat("x", vault.MaxDescription+1)},
	} {
		if c.Validate() == nil {
			t.Errorf("create %s: accepted", name)
		}
	}
	if (vault.Create{Name: "company", Title: "Company"}).Validate() != nil {
		t.Error("valid create refused")
	}
	if (vault.Update{}).Validate() == nil {
		t.Error("empty update accepted")
	}
	if (vault.Revert{Vault: v, Entry: identity.NewEntryID(), Version: 0, Provenance: prov}).Validate() == nil {
		t.Error("revert to version 0 accepted")
	}
}

// A body that looks like a credential is refused, and the refusal never
// repeats it.
func TestBodiesWithSecretsAreRefused(t *testing.T) {
	for name, secret := range map[string]string{
		"anthropic": "sk-ant-" + strings.Repeat("a", 40),
		"private":   "-----BEGIN RSA PRIVATE KEY-----",
		"github":    "ghp_" + strings.Repeat("A", 36),
	} {
		err := vault.ValidBody("notes: " + secret + " ok")
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the refusal repeats the secret: %v", name, err)
		}
	}
	if err := vault.ValidBody("Open 9 to 5. Call +34600000000 or write to hello@example.com."); err != nil {
		t.Errorf("plain text refused: %v", err)
	}
}
