// Package permission holds Seaport's permissions on its IAMKit resource.
// iam/permissions.json is the single source: permission_gen.go (this
// package) and web/src/lib/permissions.gen.ts are generated from it. After
// editing the manifest run `go generate ./internal/permission`, then sync
// IAMKit with scripts/iamkit-provision.sh (README "IAM mode").
//
// Every mode uses these permissions: none and token mode admins hold All,
// iam mode admins hold what their IAMKit roles grant.
package permission

//go:generate go run ./gen -root ../..

// Role is a role on the Seaport resource, as declared in the manifest.
type Role struct {
	Name        string
	Permissions []string
}

// Has reports whether perms contains p.
func Has(perms []string, p string) bool {
	for _, have := range perms {
		if have == p {
			return true
		}
	}
	return false
}
