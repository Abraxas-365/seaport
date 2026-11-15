package auth

import (
	"strings"
	"testing"
)

func TestInviteValidate(t *testing.T) {
	ok := Invite{Email: "ana@example.com", Role: "Viewer"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid invite: %v", err)
	}
	for name, in := range map[string]Invite{
		"empty email":        {Role: "Viewer"},
		"no at sign":         {Email: "ana.example.com", Role: "Viewer"},
		"display name":       {Email: "Ana <ana@example.com>", Role: "Viewer"},
		"too long":           {Email: strings.Repeat("a", MaxEmail) + "@example.com", Role: "Viewer"},
		"unknown role":       {Email: "ana@example.com", Role: "Owner"},
		"empty role":         {Email: "ana@example.com"},
		"role is case-bound": {Email: "ana@example.com", Role: "viewer"},
	} {
		if err := in.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSetAccessValidate(t *testing.T) {
	for _, role := range []string{"Admin", "Operator", "Viewer"} {
		if err := (SetAccess{Role: role}).Validate(); err != nil {
			t.Errorf("%s: %v", role, err)
		}
	}
	for _, role := range []string{"", "admin", "Organization owner"} {
		if err := (SetAccess{Role: role}).Validate(); err == nil {
			t.Errorf("%q accepted", role)
		}
	}
}

func TestNewAPIKeyValidate(t *testing.T) {
	for _, in := range []NewAPIKey{
		{Name: "ci", Role: "Operator"},
		{Name: "ci", Role: "Operator", ExpiresIn: "never"},
		{Name: "ci", Role: "Viewer", ExpiresIn: "1h"},
		{Name: "ci", Role: "Viewer", ExpiresIn: "8760h"},
	} {
		if err := in.Validate(); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
	for name, in := range map[string]NewAPIKey{
		"no name":      {Role: "Viewer"},
		"blank name":   {Name: "  ", Role: "Viewer"},
		"long name":    {Name: strings.Repeat("n", MaxKeyName+1), Role: "Viewer"},
		"bad role":     {Name: "ci", Role: "root"},
		"too short":    {Name: "ci", Role: "Viewer", ExpiresIn: "59m"},
		"too long":     {Name: "ci", Role: "Viewer", ExpiresIn: "8761h"},
		"not duration": {Name: "ci", Role: "Viewer", ExpiresIn: "soon"},
		"negative":     {Name: "ci", Role: "Viewer", ExpiresIn: "-2h"},
	} {
		if err := in.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAccessOf(t *testing.T) {
	if got := AccessOf(nil); got != (Access{}) {
		t.Fatalf("no permissions: %+v", got)
	}
	viewer := AccessOf([]string{"iam:org:read", OrgMembersRead, OrgRolesRead})
	if !viewer.Members || viewer.Invite || viewer.AssignRoles || viewer.Remove || viewer.KeysRead || viewer.KeysWrite {
		t.Fatalf("organization viewer: %+v", viewer)
	}
	admin := AccessOf([]string{OrgMembersRead, OrgMembersWrite, OrgRolesRead, OrgRolesAssign, OrgInvitationsWrite, OrgAPIKeysRead, OrgAPIKeysWrite})
	if admin != (Access{Members: true, Invite: true, AssignRoles: true, Remove: true, KeysRead: true, KeysWrite: true}) {
		t.Fatalf("member admin: %+v", admin)
	}
	// Inviting needs the right to assign the role the invitation carries.
	if AccessOf([]string{OrgInvitationsWrite}).Invite {
		t.Fatal("invite without roles:assign")
	}
	// Listing members needs the roles to name them.
	if AccessOf([]string{OrgMembersRead}).Members {
		t.Fatal("members without roles:read")
	}
}
