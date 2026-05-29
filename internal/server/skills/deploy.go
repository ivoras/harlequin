package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// manifestName is the hash manifest stored in the data dir.
const manifestName = "skills.hashes.json"

// Deploy syncs baked-in skills (rooted under "skills/" in bakedFS) into destDir.
// Files unchanged since the last deploy (per the manifest) are overwritten with
// the new baked version; user-edited files are preserved.
func Deploy(bakedFS fs.FS, destDir, dataDir string) error {
	manifestPath := filepath.Join(dataDir, manifestName)
	manifest := loadManifest(manifestPath)
	newManifest := map[string]string{}

	err := fs.WalkDir(bakedFS, "skills", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, "skills/")
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
			return os.WriteFile(dest, baked, 0o644)
		default:
			// Edited on disk -> preserve, warn.
			log.Printf("skills: preserving locally edited %s (not overwriting on update)", rel)
			return nil
		}
	})
	if err != nil {
		return err
	}

	return saveManifest(manifestPath, newManifest)
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
