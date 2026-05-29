package main

import (
	"fmt"
	"log"
	"os"

	"github.com/ivoras/harlequin/internal/server/sessionlog"
)

func runPrintTrajectory(args []string) {
	verbose := false
	color := true
	var path string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--verbose", "-v":
			verbose = true
		case "--no-color":
			color = false
		case "--help", "-h":
			fmt.Fprintln(os.Stderr, "usage: harlequin-server print-trajectory [--verbose] [--no-color] <file.jsonl>")
			os.Exit(0)
		default:
			if path != "" {
				log.Fatalf("print-trajectory: unexpected argument %q", args[i])
			}
			path = args[i]
		}
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "print-trajectory: JSONL file path required")
		os.Exit(2)
	}

	events, err := sessionlog.ReadFile(path)
	if err != nil {
		log.Fatalf("print-trajectory: %v", err)
	}
	sessionlog.Print(os.Stdout, events, sessionlog.PrintOptions{
		Verbose: verbose,
		Color:   color,
	})
}
