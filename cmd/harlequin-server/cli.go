package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/db"
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
	case "createuser", "changepassword", "deleteuser", "listusers", "print-trajectory":
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
  harlequin-server createuser [flags] <username>
  harlequin-server changepassword [flags] <username>
  harlequin-server deleteuser [flags] <username>
  harlequin-server listusers [flags]
  harlequin-server print-trajectory [flags] <file.jsonl>
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

print-trajectory flags:
  --verbose, -v     include token/thinking delta events
  --no-color        disable ANSI colors

Examples:
  harlequin-server --config server.yaml
  harlequin-server createuser --owner --password secret owner
  harlequin-server createuser --admin --password secret alice
  harlequin-server changepassword alice --password newsecret
  harlequin-server listusers
  harlequin-server deleteuser alice
  harlequin-server print-trajectory data/sessions/1.42.jsonl
  harlequin-server print-trajectory -v --no-color trajectory.jsonl
`)
}

type cmdFlags struct {
	config   string
	password string
	admin    bool
	owner    bool
	username string
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
			if f.username != "" {
				return f, fmt.Errorf("unexpected argument %q", a)
			}
			f.username = a
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
	if f.username == "" {
		fmt.Fprintln(os.Stderr, "createuser: username required")
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
	u, err := store.auth.CreateUser(context.Background(), f.username, pw, role)
	if err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			log.Fatalf("createuser: user %q already exists", f.username)
		}
		log.Fatalf("createuser: %v", err)
	}
	fmt.Printf("created user %s (id=%d, role=%s)\n", u.Username, u.ID, u.Role)
}

func runChangePassword(args []string) {
	f, err := parseCmdFlags(args, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "changepassword:", err)
		os.Exit(2)
	}
	if f.username == "" {
		fmt.Fprintln(os.Stderr, "changepassword: username required")
		printUsage()
		os.Exit(2)
	}

	pw := resolvePassword(f.password)
	store := openAuthStore(f.config)
	defer store.db.Close()

	if err := store.auth.ChangePassword(context.Background(), f.username, pw); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			log.Fatalf("changepassword: user %q not found", f.username)
		}
		log.Fatalf("changepassword: %v", err)
	}
	fmt.Printf("password changed for %s (existing API tokens revoked)\n", f.username)
}

func runDeleteUser(args []string) {
	f, err := parseCmdFlags(args, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "deleteuser:", err)
		os.Exit(2)
	}
	if f.username == "" {
		fmt.Fprintln(os.Stderr, "deleteuser: username required")
		printUsage()
		os.Exit(2)
	}

	store := openAuthStore(f.config)
	defer store.db.Close()

	id, err := store.auth.DeleteUser(context.Background(), f.username)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			log.Fatalf("deleteuser: user %q not found", f.username)
		}
		log.Fatalf("deleteuser: %v", err)
	}
	// Remove the user's per-user database and uploaded files.
	dir := storage.UserDir(store.dataDir, id)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("deleteuser: removed user %q (id=%d) but failed to delete %s: %v", f.username, id, dir, err)
	} else {
		fmt.Printf("deleted user %s (id=%d) and its data directory\n", f.username, id)
	}
}

func runListUsers(args []string) {
	f, err := parseCmdFlags(args, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listusers:", err)
		os.Exit(2)
	}
	if f.username != "" {
		fmt.Fprintln(os.Stderr, "listusers: unexpected argument", f.username)
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
	fmt.Printf("%-5s  %-20s  %-6s  %s\n", "ID", "USERNAME", "ROLE", "CREATED")
	for _, u := range users {
		fmt.Printf("%-5d  %-20s  %-6s  %s\n", u.ID, u.Username, u.Role, u.CreatedAt.UTC().Format("2006-01-02T15:04Z"))
	}
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
