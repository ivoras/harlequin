package memory

import (
	"strconv"
	"strings"
)

// Composite-id scopes. A memory's public id encodes which database file holds
// it: "u.<localid>" for a per-user database, "s.<localid>" for the shared
// database. Conflict-row ids reuse the same scheme to encode which database
// holds the conflict record.
const (
	scopeUser    = "user"
	scopeShared  = "shared"
	scopeProject = "project"
)

// encodeID builds a composite id from a scope and a local rowid.
func encodeID(scope string, local int64) string {
	prefix := "u"
	switch scope {
	case scopeShared:
		prefix = "s"
	case scopeProject:
		prefix = "p"
	}
	return prefix + "." + strconv.FormatInt(local, 10)
}

// decodeID splits a composite id back into its scope and local rowid.
func decodeID(id string) (scope string, local int64, ok bool) {
	prefix, num, found := strings.Cut(id, ".")
	if !found {
		return "", 0, false
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return "", 0, false
	}
	switch prefix {
	case "u":
		return scopeUser, n, true
	case "s":
		return scopeShared, n, true
	case "p":
		return scopeProject, n, true
	default:
		return "", 0, false
	}
}
