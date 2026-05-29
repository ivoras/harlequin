// Package harlequin exposes assets embedded into the server binary, notably the
// baked-in skills that are deployed to the server data directory on install/update.
package harlequin

import "embed"

//go:embed skills
var bakedSkillsFS embed.FS

// BakedSkillsFS returns the embedded filesystem rooted at the repository, so the
// skills live under the "skills/" prefix.
func BakedSkillsFS() embed.FS {
	return bakedSkillsFS
}
