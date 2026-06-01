package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Deploy syncs a baked-in asset tree (rooted under srcRoot, e.g. "skills" or
// "hats", in bakedFS) into destDir. Files unchanged since the last deploy (per
// the per-root manifest "<srcRoot>.hashes.json" in dataDir) are overwritten with
// the new baked version; user-edited files are preserved. Any file type is
// handled (raw bytes), so binaries and scripts deploy verbatim.
func Deploy(bakedFS fs.FS, srcRoot, destDir, dataDir string) error {
	manifestPath := filepath.Join(dataDir, srcRoot+".hashes.json")
	manifest := loadManifest(manifestPath)
	newManifest := map[string]string{}

	var created, updated, preserved []string

	err := fs.WalkDir(bakedFS, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, srcRoot+"/")
		baked, err := fs.ReadFile(bakedFS, p)
		if err != nil {
			return err
		}
		bakedHash := hashBytes(baked)
		newManifest[rel] = bakedHash

		dest := filepath.Join(destDir, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}

		onDisk, readErr := os.ReadFile(dest)
		if os.IsNotExist(readErr) {
			created = append(created, rel)
			return os.WriteFile(dest, baked, 0o644)
		}
		if readErr != nil {
			return readErr
		}

		diskHash := hashBytes(onDisk)
		prevHash, known := manifest[rel]
		switch {
		case diskHash == bakedHash:
			// Already up to date.
			return nil
		case known && diskHash == prevHash:
			// Unchanged since last deploy -> replace with new baked version.
			updated = append(updated, rel)
			return os.WriteFile(dest, baked, 0o644)
		default:
			// Edited on disk -> preserve, warn.
			preserved = append(preserved, rel)
			log.Printf("%s: preserving locally edited %s (not overwriting on update)", srcRoot, rel)
			return nil
		}
	})
	if err != nil {
		return err
	}

	logDeployed(srcRoot, destDir, created, updated, preserved)
	return saveManifest(manifestPath, newManifest)
}

// logDeployed reports which embedded asset files were written to disk this run.
func logDeployed(srcRoot, destDir string, created, updated, preserved []string) {
	sort.Strings(created)
	sort.Strings(updated)
	if len(created) > 0 {
		log.Printf("%s: deployed %d new file(s) into %s: %s", srcRoot, len(created), destDir, strings.Join(created, ", "))
	}
	if len(updated) > 0 {
		log.Printf("%s: updated %d file(s) in %s: %s", srcRoot, len(updated), destDir, strings.Join(updated, ", "))
	}
	if len(created) == 0 && len(updated) == 0 {
		log.Printf("%s: assets up to date in %s (%d preserved local edit(s))", srcRoot, destDir, len(preserved))
	}
}

func loadManifest(path string) map[string]string {
	m := map[string]string{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func saveManifest(path string, m map[string]string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
