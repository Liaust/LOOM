package projectcontracts

import (
	"regexp"
	"strings"
)

var protonIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}={0,2}$`)
var protonField = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

// Only explicit ID/field references. Execution uses --share-id/--item-id rather
// than Proton's URI name lookup, which can otherwise select an ambiguous title.
func ParseApplicationProtonReference(ref string) (share, item, field string, ok bool) {
	if !strings.HasPrefix(ref, "pass://") {
		return
	}
	parts := strings.Split(strings.TrimPrefix(ref, "pass://"), "/")
	if len(parts) != 3 || !protonIdentifier.MatchString(parts[0]) || !protonIdentifier.MatchString(parts[1]) || !protonField.MatchString(parts[2]) || parts[2] == "totp" {
		return
	}
	return parts[0], parts[1], parts[2], true
}

// Generation names a vault, not an existing item. The helper binds the new
// item to the application/credential and retains its ID for every retry.
func ApplicationCredentialSourceShare(source string) (share string, generate, ok bool) {
	if share, _, _, ok = ParseApplicationProtonReference(source); ok {
		return share, false, true
	}
	parts := strings.Split(strings.TrimPrefix(source, "pass+generate://"), "/")
	if !strings.HasPrefix(source, "pass+generate://") || len(parts) != 2 || !protonIdentifier.MatchString(parts[0]) || parts[1] != "password" {
		return "", false, false
	}
	return parts[0], true, true
}
