// Package harlequin exposes assets embedded into the server binary: the baked-in
// skills and hats that are deployed to the server data directory on install/update.
package harlequin

import "embed"

//go:embed skills hats
var bakedFS embed.FS

// BakedFS returns the embedded filesystem rooted at the repository, so the
// assets live under the "skills/" and "hats/" prefixes.
func BakedFS() embed.FS {
	return bakedFS
}
