package skills

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
)

// seedSpec parametrizes the baked-asset seeder over the skills and hats table
// sets. Table/column names are compile-time constants, never user input.
type seedSpec struct {
	label     string // log prefix ("skills"/"hats")
	itemTable string // skills / hats
	fileTable string // skill_files / hat_files
	keyCol    string // skill_name / hat_name
	hashTable string // skill_seed_hashes / hat_seed_hashes
	// descOf extracts the item's listed description from its files.
	descOf func(files map[string]string) string
	// write replaces the whole item in the database (upsert + files).
	write func(ctx context.Context, db *sql.DB, name, desc string, files map[string]string) error
}

var skillSeedSpec = seedSpec{
	label: "skills", itemTable: "skills", fileTable: "skill_files",
	keyCol: "skill_name", hashTable: "skill_seed_hashes",
	descOf: func(files map[string]string) string { return descriptionOf(files["SKILL.md"]) },
	write: func(ctx context.Context, db *sql.DB, name, desc string, files map[string]string) error {
		return writeSkill(ctx, db, name, desc, 0, files)
	},
}

var hatSeedSpec = seedSpec{
	label: "hats", itemTable: "hats", fileTable: "hat_files",
	keyCol: "hat_name", hashTable: "hat_seed_hashes",
	descOf: func(files map[string]string) string {
		fm, _ := splitHatFrontmatter(files[hatPromptFile])
		return fm.Description
	},
	write: func(ctx context.Context, db *sql.DB, name, desc string, files map[string]string) error {
		return writeHat(ctx, db, name, desc, 0, files)
	},
}

// Seed installs the baked-in skills (under "skills/<name>/..." in the embedded
// FS) and hats (under "hats/<name>/...") into the shared database. It is
// hash-guarded via the *_seed_hashes tables: unchanged baked files are
// (re)written, but a file a human has edited in the DB since the last seed is
// preserved (same policy as the old on-disk deploy). Top-level baked files
// (e.g. skills/system_prompt.md) are not skills and are skipped — they are read
// straight from the binary by RenderFile.
func (m *Manager) Seed(ctx context.Context) error {
	if err := SeedSkills(ctx, m.shared, m.baked, "skills"); err != nil {
		return err
	}
	return SeedHats(ctx, m.shared, m.baked, "hats")
}

// SeedSkills seeds the baked skill tree into the shared database.
func SeedSkills(ctx context.Context, shared *sql.DB, baked fs.FS, root string) error {
	return seedTree(ctx, shared, baked, root, skillSeedSpec)
}

// SeedHats seeds the baked hat tree into the shared database.
func SeedHats(ctx context.Context, shared *sql.DB, baked fs.FS, root string) error {
	return seedTree(ctx, shared, baked, root, hatSeedSpec)
}

// seedTree is the shared core: walk the baked tree, group files by item name,
// and reconcile each item into the database per the spec.
func seedTree(ctx context.Context, shared *sql.DB, baked fs.FS, root string, spec seedSpec) error {
	// Group baked files by item name: <root>/<name>/<sub...> -> content.
	bakedItems := map[string]map[string]string{}
	err := fs.WalkDir(baked, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, root+"/")
		name, sub, ok := strings.Cut(rel, "/")
		if !ok || name == "" || sub == "" {
			return nil // top-level file (e.g. system_prompt.md), not an item
		}
		content, rerr := fs.ReadFile(baked, p)
		if rerr != nil {
			return rerr
		}
		if bakedItems[name] == nil {
			bakedItems[name] = map[string]string{}
		}
		bakedItems[name][sub] = string(content)
		return nil
	})
	if err != nil {
		return err
	}

	prev := loadSeedHashes(ctx, shared, spec.hashTable)
	newHashes := map[string]string{}
	var created, updated, preserved []string

	names := make([]string, 0, len(bakedItems))
	for n := range bakedItems {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		files := bakedItems[name]
		var exists int
		if err := shared.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE name = ?`, spec.itemTable), name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			// New item: install all baked files as-is.
			if err := spec.write(ctx, shared, name, spec.descOf(files), files); err != nil {
				return err
			}
			for sub, content := range files {
				newHashes[name+"/"+sub] = hashBytes([]byte(content))
			}
			created = append(created, name)
			continue
		}

		// Existing item: reconcile file by file, preserving human edits.
		changed := false
		for sub, content := range files {
			key := name + "/" + sub
			bakedHash := hashBytes([]byte(content))
			newHashes[key] = bakedHash

			dbContent, present := readItemFile(ctx, shared, spec, name, sub)
			switch {
			case !present:
				writeItemFile(ctx, shared, spec, name, sub, content)
				changed = true
			case hashBytes([]byte(dbContent)) == bakedHash:
				// up to date
			case prev[key] == hashBytes([]byte(dbContent)):
				// unchanged since last seed → adopt the new baked version
				writeItemFile(ctx, shared, spec, name, sub, content)
				changed = true
			default:
				preserved = append(preserved, key)
			}
		}
		if changed {
			// Refresh the listed description from the effective files.
			effective, _ := readAllItemFiles(ctx, shared, spec, name)
			_, _ = shared.ExecContext(ctx,
				fmt.Sprintf(`UPDATE %s SET description = ? WHERE name = ?`, spec.itemTable),
				spec.descOf(effective), name)
			updated = append(updated, name)
		}
	}

	logSeeded(spec.label, created, updated, preserved)
	return saveSeedHashes(ctx, shared, spec.hashTable, newHashes)
}

// descriptionOf extracts the frontmatter description from a SKILL.md (best effort).
func descriptionOf(md string) string {
	if md == "" {
		return ""
	}
	fm, _, err := parseFrontmatter(md)
	if err != nil {
		return ""
	}
	return fm.Description
}

func readItemFile(ctx context.Context, db *sql.DB, spec seedSpec, name, relpath string) (string, bool) {
	var content []byte
	err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT content FROM %s WHERE %s = ? AND relpath = ?`, spec.fileTable, spec.keyCol),
		name, relpath).Scan(&content)
	if err != nil {
		return "", false
	}
	return string(content), true
}

func readAllItemFiles(ctx context.Context, db *sql.DB, spec seedSpec, name string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		fmt.Sprintf(`SELECT relpath, content FROM %s WHERE %s = ?`, spec.fileTable, spec.keyCol), name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := map[string]string{}
	for rows.Next() {
		var rel string
		var content []byte
		if err := rows.Scan(&rel, &content); err != nil {
			return nil, err
		}
		files[rel] = string(content)
	}
	return files, rows.Err()
}

func writeItemFile(ctx context.Context, db *sql.DB, spec seedSpec, name, relpath, content string) {
	_, _ = db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s(%s, relpath, content) VALUES (?, ?, ?)
		 ON CONFLICT(%s, relpath) DO UPDATE SET content = excluded.content`,
			spec.fileTable, spec.keyCol, spec.keyCol),
		name, relpath, []byte(content))
}

func loadSeedHashes(ctx context.Context, db *sql.DB, table string) map[string]string {
	out := map[string]string{}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT relpath, hash FROM %s`, table))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var rel, h string
		if rows.Scan(&rel, &h) == nil {
			out[rel] = h
		}
	}
	return out
}

func saveSeedHashes(ctx context.Context, db *sql.DB, table string, hashes map[string]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, table)); err != nil {
		return err
	}
	for rel, h := range hashes {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`INSERT INTO %s(relpath, hash) VALUES (?, ?)`, table), rel, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func logSeeded(label string, created, updated, preserved []string) {
	sort.Strings(created)
	sort.Strings(updated)
	if len(created) > 0 {
		log.Printf("%s: seeded %d new item(s) into shared DB: %s", label, len(created), strings.Join(created, ", "))
	}
	if len(updated) > 0 {
		log.Printf("%s: updated %d item(s) in shared DB: %s", label, len(updated), strings.Join(updated, ", "))
	}
	if len(created) == 0 && len(updated) == 0 {
		log.Printf("%s: shared DB up to date (%d preserved local edit(s))", label, len(preserved))
	}
}
