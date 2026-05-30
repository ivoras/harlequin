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
	case "print-trajectory", "print-trajetory":
		runPrintTrajectory(args[1:])
		return true
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	}
	return false
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Harlequin server

Usage:
  harlequin-server [server flags]
  harlequin-server createuser [flags] <username>
  harlequin-server changepassword [flags] <username>
  harlequin-server print-trajectory [flags] <file.jsonl>
  harlequin-server help

Server flags:
  --config path   server config YAML (default server.yaml)

createuser flags:
  --config path     server config YAML (default server.yaml)
  --password pwd    password (or set HARLEQUIN_NEW_PASSWORD)
  --admin           create as admin (default: user)

changepassword flags:
  --config path     server config YAML (default server.yaml)
  --password pwd    new password (or set HARLEQUIN_NEW_PASSWORD)

print-trajectory flags:
  --verbose, -v     include token/thinking delta events
  --no-color        disable ANSI colors

Examples:
  harlequin-server --config server.yaml
  harlequin-server createuser --admin --password secret admin
  harlequin-server changepassword alice --password newsecret
  harlequin-server print-trajectory data/sessions/1/42.jsonl
  harlequin-server print-trajectory -v --no-color trajectory.jsonl
`)
}

type cmdFlags struct {
	config   string
	password string
	admin    bool
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

	role := "user"
	if f.admin {
		role = "admin"
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
	auth *auth.Store
	db   interface{ Close() error }
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
	return authDeps{auth: auth.NewStore(database), db: database}
}
