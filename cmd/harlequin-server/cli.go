package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func dispatchCLI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "createuser":
		runCreateUser(args[1:])
		return true
	case "changepassword":
		runChangePassword(args[1:])
		return true
	case "deleteuser":
		runDeleteUser(args[1:])
		return true
	case "listusers":
		runListUsers(args[1:])
		return true
	case "print-trajectory":
		runPrintTrajectory(args[1:])
		return true
	case "backfill-slot-keys":
		runBackfillSlotKeys(args[1:])
		return true
	case "reindex-vectors":
		runReindexVectors(args[1:])
		return true
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	}
	return false
}

// isSubcommand reports whether name is one of the CLI subcommands. Subcommands
// must be the first argument (before any flags).
func isSubcommand(name string) bool {
	switch name {
	case "createuser", "changepassword", "deleteuser", "listusers", "print-trajectory", "backfill-slot-keys", "reindex-vectors":
		return true
	}
	return false
}

// rejectStrayArgs aborts with a helpful error when the server command is given
// leftover positional arguments — most often a subcommand placed after the
// flags (e.g. "--config server.yaml createuser"), which would otherwise be
// silently ignored while the server starts anyway.
func rejectStrayArgs(extra []string) {
	if len(extra) == 0 {
		return
	}
	if isSubcommand(extra[0]) {
		fmt.Fprintf(os.Stderr,
			"harlequin-server: %q is a subcommand and must come before any flags, e.g.:\n  harlequin-server %s [flags] <args>\n\n",
			extra[0], extra[0])
	} else {
		fmt.Fprintf(os.Stderr, "harlequin-server: unexpected argument(s): %s\n\n", strings.Join(extra, " "))
	}
	printUsage()
	os.Exit(2)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Harlequin server

Usage:
  harlequin-server [server flags]
  harlequin-server createuser [flags] <email>
  harlequin-server changepassword [flags] <email>
  harlequin-server deleteuser [flags] <email>
  harlequin-server listusers [flags]
  harlequin-server print-trajectory [flags] <file.jsonl>
  harlequin-server backfill-slot-keys [flags]
  harlequin-server reindex-vectors [flags]
  harlequin-server help

Server flags:
  --config path   server config YAML (default server.yaml)

createuser flags:
  --config path     server config YAML (default server.yaml)
  --password pwd    password (or set HARLEQUIN_NEW_PASSWORD)
  --owner           create as owner (highest role; can manage users)
  --admin           create as admin (default: user)

changepassword flags:
  --config path     server config YAML (default server.yaml)
  --password pwd    new password (or set HARLEQUIN_NEW_PASSWORD)

deleteuser flags:
  --config path     server config YAML (default server.yaml)

listusers flags:
  --config path     server config YAML (default server.yaml)

backfill-slot-keys flags:
  --config path     server config YAML (default server.yaml)
                    re-embeds every slot key (shared + all users) with the
                    humanized form; run once after upgrading.

reindex-vectors flags:
  --config path     server config YAML (default server.yaml)
                    drops and recreates the vec0 tables (shared + all users)
                    and re-embeds every memory, slot key and document chunk.
                    Run once after changing the embedding model or the vector
                    distance metric.

print-trajectory flags:
  --verbose, -v     include token/thinking delta events
  --no-color        disable ANSI colors

Examples:
  harlequin-server --config server.yaml
  harlequin-server createuser --owner --password secret owner@example.com
  harlequin-server createuser --admin --password secret alice@example.com
  harlequin-server changepassword alice@example.com --password newsecret
  harlequin-server listusers
  harlequin-server deleteuser alice@example.com
  harlequin-server print-trajectory data/sessions/00001.00042.jsonl
  harlequin-server print-trajectory -v --no-color trajectory.jsonl
`)
}

type cmdFlags struct {
	config   string
	password string
	admin    bool
	owner    bool
	email    string
}

func parseCmdFlags(args []string, allowAdmin bool) (cmdFlags, error) {
	f := cmdFlags{config: "server.yaml"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--config":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--config requires a value")
			}
			i++
			f.config = args[i]
		case "--password":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--password requires a value")
			}
			i++
			f.password = args[i]
		case "--admin":
			if !allowAdmin {
				return f, fmt.Errorf("unknown flag %s", a)
			}
			f.admin = true
		case "--owner":
			if !allowAdmin {
				return f, fmt.Errorf("unknown flag %s", a)
			}
			f.owner = true
		default:
			if strings.HasPrefix(a, "-") {
				return f, fmt.Errorf("unknown flag %s", a)
			}
			if f.email != "" {
				return f, fmt.Errorf("unexpected argument %q", a)
			}
			f.email = a
		}
	}
	return f, nil
}

func runCreateUser(args []string) {
	f, err := parseCmdFlags(args, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "createuser:", err)
		os.Exit(2)
	}
	if f.email == "" {
		fmt.Fprintln(os.Stderr, "createuser: email required")
		printUsage()
		os.Exit(2)
	}

	pw := resolvePassword(f.password)
	store := openAuthStore(f.config)
	defer store.db.Close()

	role := types.RoleUser
	switch {
	case f.owner:
		role = types.RoleOwner
	case f.admin:
		role = types.RoleAdmin
	}
	u, err := store.auth.CreateUser(context.Background(), f.email, pw, role)
	if err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			log.Fatalf("createuser: user %q already exists", f.email)
		}
		log.Fatalf("createuser: %v", err)
	}
	fmt.Printf("created user %s (id=%d, role=%s)\n", u.Email, u.ID, u.Role)
}

func runChangePassword(args []string) {
	f, err := parseCmdFlags(args, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "changepassword:", err)
		os.Exit(2)
	}
	if f.email == "" {
		fmt.Fprintln(os.Stderr, "changepassword: email required")
		printUsage()
		os.Exit(2)
	}

	pw := resolvePassword(f.password)
	store := openAuthStore(f.config)
	defer store.db.Close()

	if err := store.auth.ChangePassword(context.Background(), f.email, pw); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			log.Fatalf("changepassword: user %q not found", f.email)
		}
		log.Fatalf("changepassword: %v", err)
	}
	fmt.Printf("password changed for %s (existing API tokens revoked)\n", f.email)
}

func runDeleteUser(args []string) {
	f, err := parseCmdFlags(args, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "deleteuser:", err)
		os.Exit(2)
	}
	if f.email == "" {
		fmt.Fprintln(os.Stderr, "deleteuser: email required")
		printUsage()
		os.Exit(2)
	}

	store := openAuthStore(f.config)
	defer store.db.Close()

	id, err := store.auth.DeleteUser(context.Background(), f.email)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			log.Fatalf("deleteuser: user %q not found", f.email)
		}
		log.Fatalf("deleteuser: %v", err)
	}
	// Remove the user's per-user database and uploaded files.
	dir := storage.UserDir(store.dataDir, id)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("deleteuser: removed user %q (id=%d) but failed to delete %s: %v", f.email, id, dir, err)
	} else {
		fmt.Printf("deleted user %s (id=%d) and its data directory\n", f.email, id)
	}
}

func runListUsers(args []string) {
	f, err := parseCmdFlags(args, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listusers:", err)
		os.Exit(2)
	}
	if f.email != "" {
		fmt.Fprintln(os.Stderr, "listusers: unexpected argument", f.email)
		printUsage()
		os.Exit(2)
	}

	store := openAuthStore(f.config)
	defer store.db.Close()

	users, err := store.auth.ListUsers(context.Background())
	if err != nil {
		log.Fatalf("listusers: %v", err)
	}
	if len(users) == 0 {
		fmt.Println("no users")
		return
	}
	fmt.Printf("%-5s  %-20s  %-6s  %s\n", "ID", "EMAIL", "ROLE", "CREATED")
	for _, u := range users {
		fmt.Printf("%-5d  %-20s  %-6s  %s\n", u.ID, u.Email, u.Role, u.CreatedAt.UTC().Format("2006-01-02T15:04Z"))
	}
}

func runBackfillSlotKeys(args []string) {
	f, err := parseCmdFlags(args, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backfill-slot-keys:", err)
		os.Exit(2)
	}
	if f.email != "" {
		fmt.Fprintln(os.Stderr, "backfill-slot-keys: unexpected argument", f.email)
		printUsage()
		os.Exit(2)
	}

	cfg, err := config.Load(f.config)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	store, err := storage.New(cfg.DataDir, cfg.DBPath, cfg.Embeddings.Dim)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer store.Close()
	embedder := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim)
	mem := memory.NewStore(store.Shared, embedder)

	ctx := context.Background()
	total := 0
	n, err := mem.BackfillSlotKeyEmbeddings(ctx, store.Shared)
	if err != nil {
		log.Fatalf("backfill-slot-keys (shared): %v", err)
	}
	total += n
	fmt.Printf("shared: reindexed %d slot key(s)\n", n)

	if err := store.EachUser(ctx, func(uid int64, udb *sql.DB) error {
		un, err := mem.BackfillSlotKeyEmbeddings(ctx, udb)
		if err != nil {
			return fmt.Errorf("user %d: %w", uid, err)
		}
		if un > 0 {
			fmt.Printf("user %d: reindexed %d slot key(s)\n", uid, un)
		}
		total += un
		return nil
	}); err != nil {
		log.Fatalf("backfill-slot-keys: %v", err)
	}
	fmt.Printf("done: reindexed %d slot key(s) total\n", total)
}

func runReindexVectors(args []string) {
	f, err := parseCmdFlags(args, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reindex-vectors:", err)
		os.Exit(2)
	}
	if f.email != "" {
		fmt.Fprintln(os.Stderr, "reindex-vectors: unexpected argument", f.email)
		printUsage()
		os.Exit(2)
	}

	cfg, err := config.Load(f.config)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	store, err := storage.New(cfg.DataDir, cfg.DBPath, cfg.Embeddings.Dim)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer store.Close()
	embedder := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim)
	mem := memory.NewStore(store.Shared, embedder)
	docs := documents.NewStore(store.Shared, embedder)
	ctx := context.Background()

	// Shared: memories, slot keys, and document chunks.
	if err := db.RecreateVectorTables(store.Shared, db.Shared, cfg.Embeddings.Dim); err != nil {
		log.Fatalf("reindex-vectors (shared tables): %v", err)
	}
	smem, err := mem.ReindexMemoryVectors(ctx, store.Shared)
	if err != nil {
		log.Fatalf("reindex-vectors (shared memories): %v", err)
	}
	sslot, err := mem.BackfillSlotKeyEmbeddings(ctx, store.Shared)
	if err != nil {
		log.Fatalf("reindex-vectors (shared slots): %v", err)
	}
	sdoc, err := docs.ReindexChunkVectors(ctx)
	if err != nil {
		log.Fatalf("reindex-vectors (shared doc chunks): %v", err)
	}
	fmt.Printf("shared: %d memory(ies), %d slot(s), %d doc chunk(s)\n", smem, sslot, sdoc)

	// Each user: memories and slot keys (documents are shared-only).
	if err := store.EachUser(ctx, func(uid int64, udb *sql.DB) error {
		if err := db.RecreateVectorTables(udb, db.User, cfg.Embeddings.Dim); err != nil {
			return fmt.Errorf("user %d tables: %w", uid, err)
		}
		umem, err := mem.ReindexMemoryVectors(ctx, udb)
		if err != nil {
			return fmt.Errorf("user %d memories: %w", uid, err)
		}
		uslot, err := mem.BackfillSlotKeyEmbeddings(ctx, udb)
		if err != nil {
			return fmt.Errorf("user %d slots: %w", uid, err)
		}
		if umem > 0 || uslot > 0 {
			fmt.Printf("user %d: %d memory(ies), %d slot(s)\n", uid, umem, uslot)
		}
		return nil
	}); err != nil {
		log.Fatalf("reindex-vectors: %v", err)
	}

	// Each project: memories, slot keys, and document chunks (its own database).
	if err := store.EachProject(ctx, func(pid int64, pdb *sql.DB) error {
		if err := db.RecreateVectorTables(pdb, db.Project, cfg.Embeddings.Dim); err != nil {
			return fmt.Errorf("project %d tables: %w", pid, err)
		}
		pmem, err := mem.ReindexMemoryVectors(ctx, pdb)
		if err != nil {
			return fmt.Errorf("project %d memories: %w", pid, err)
		}
		pslot, err := mem.BackfillSlotKeyEmbeddings(ctx, pdb)
		if err != nil {
			return fmt.Errorf("project %d slots: %w", pid, err)
		}
		pdoc, err := documents.NewStore(pdb, embedder).ReindexChunkVectors(ctx)
		if err != nil {
			return fmt.Errorf("project %d doc chunks: %w", pid, err)
		}
		if pmem > 0 || pslot > 0 || pdoc > 0 {
			fmt.Printf("project %d: %d memory(ies), %d slot(s), %d doc chunk(s)\n", pid, pmem, pslot, pdoc)
		}
		return nil
	}); err != nil {
		log.Fatalf("reindex-vectors (projects): %v", err)
	}

	fmt.Println("done")
}

func resolvePassword(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if pw := os.Getenv("HARLEQUIN_NEW_PASSWORD"); pw != "" {
		return pw
	}
	log.Fatal("password required: pass --password or set HARLEQUIN_NEW_PASSWORD")
	return ""
}

type authDeps struct {
	auth    *auth.Store
	db      interface{ Close() error }
	dataDir string
}

func openAuthStore(configPath string) authDeps {
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	database, err := db.Open(cfg.DBPath, db.System, cfg.Embeddings.Dim)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	return authDeps{auth: auth.NewStore(database), db: database, dataDir: cfg.DataDir}
}
