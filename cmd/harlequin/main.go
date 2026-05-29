// Command harlequin is the Harlequin TUI client.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	clientcfg "github.com/ivoras/harlequin/internal/client/config"
	"github.com/ivoras/harlequin/internal/client/tui"
)

func main() {
	configPath := flag.String("config", "", "path to client config YAML (default ~/.config/harlequin/client.yaml)")
	flag.Parse()

	cfg, err := clientcfg.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	model := tui.New(cfg)
	prog := tea.NewProgram(model)
	model.SetProgram(prog)

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
