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
// sets. Table names are compile-time constants, never user input.
type seedSpec struct {
	label     string     // log prefix ("skills"/"hats")
	tables    itemTables // item/file tables shared with the CRUD layer
	hashTable string     // skill_seed_hashes / hat_seed_hashes
	// descOf extracts the item's listed description from its files.
	descOf func(files map[string]string) string
}

var skillSeedSpec = seedSpec{
	label: "skills", tables: skillTables, hashTable: "skill_seed_hashes",
	descOf: func(files map[string]string) string { return descriptionOf(files["SKILL.md"]) },
}

var hatSeedSpec = seedSpec{
	label: "hats", tables: hatTables, hashTable: "hat_seed_hashes",
	descOf: func(files map[string]string) string {
		fm, _ := splitHatFrontmatter(files[hatPromptFile])
		return fm.Description
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
// and reconcile each item into the database per the spec. Any write error
// aborts before saveSeedHashes runs, so a failed write is retried on the next
// start instead of being misrecorded as a human edit.
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
		existing, err := readItemFiles(ctx, shared, spec.tables, name)
		if err != nil {
			return err
		}
		if existing == nil {
			// New item: install all baked files as-is.
			if err := writeItem(ctx, shared, spec.tables, name, spec.descOf(files), 0, files); err != nil {
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

			dbContent, present := existing[sub]
			switch {
			case !present:
				if err := writeItemFile(ctx, shared, spec.tables, name, sub, content); err != nil {
					return err
				}
				existing[sub] = content
				changed = true
			case hashBytes([]byte(dbContent)) == bakedHash:
				// up to date
			case prev[key] == hashBytes([]byte(dbContent)):
				// unchanged since last seed → adopt the new baked version
				if err := writeItemFile(ctx, shared, spec.tables, name, sub, content); err != nil {
					return err
				}
				existing[sub] = content
				changed = true
			default:
				preserved = append(preserved, key)
			}
		}
		if changed {
			// Refresh the listed description from the effective files.
			if _, err := shared.ExecContext(ctx,
				fmt.Sprintf(`UPDATE %s SET description = ? WHERE name = ?`, spec.tables.item),
				spec.descOf(existing), name); err != nil {
				return err
			}
			updated = append(updated, name)
		}
	}

	logSeeded(spec.label, created, updated, preserved)
	return saveSeedHashes(ctx, shared, spec.hashTable, newHashes)
}

// RepairDescriptions back-fills empty skill description columns from the
// stored SKILL.md frontmatter. The 0004 migrations copied old skill_overrides
// rows with description = '' (SQL can't parse YAML frontmatter), and listings
// now trust the column, so empty rows must be healed once per database.
// Idempotent; a no-op when nothing is empty.
func RepairDescriptions(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx,
		`SELECT s.name, f.content FROM skills s
		 JOIN skill_files f ON f.skill_name = s.name AND f.relpath = 'SKILL.md'
		 WHERE s.description = ''`)
	if err != nil {
		return err
	}
	fixes := map[string]string{}
	for rows.Next() {
		var name string
		var md []byte
		if err := rows.Scan(&name, &md); err != nil {
			rows.Close()
			return err
		}
		if desc := descriptionOf(string(md)); desc != "" {
			fixes[name] = desc
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for name, desc := range fixes {
		if _, err := db.ExecContext(ctx, `UPDATE skills SET description = ? WHERE name = ?`, desc, name); err != nil {
			return err
		}
	}
	if len(fixes) > 0 {
		log.Printf("skills: repaired %d empty description(s)", len(fixes))
	}
	return nil
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

// writeItemFile upserts a single file row (used only by the seed reconciler;
// regular writes replace the whole item via writeItem).
func writeItemFile(ctx context.Context, db *sql.DB, t itemTables, name, relpath, content string) error {
	_, err := db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s(%s, relpath, content) VALUES (?, ?, ?)
		 ON CONFLICT(%s, relpath) DO UPDATE SET content = excluded.content`,
			t.files, t.key, t.key),
		name, relpath, []byte(content))
	return err
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
