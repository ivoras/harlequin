// Command agentprobe drives the Harlequin agent like a real client: it reads the
// saved token/server URL from client.yaml (the same file the TUI uses), opens a
// session, sends a message, and prints a compact trace of the agent's tool
// calls and results. It exists to debug what the (small) model actually does.
//
//	go run ./cmd/agentprobe [-sess N] [-project N] [-thinking] "your message"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ivoras/harlequin/internal/client/apiclient"
	"github.com/ivoras/harlequin/internal/client/config"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func main() {
	cfgPath := flag.String("config", "client.yaml", "client config path")
	sessID := flag.Int64("sess", 0, "reuse an existing session id (0 = create new)")
	projectID := flag.Int64("project", 0, "run in this project's context (project session)")
	showThinking := flag.Bool("thinking", false, "print model thinking")
	flag.Parse()
	msg := strings.Join(flag.Args(), " ")
	if msg == "" {
		fmt.Fprintln(os.Stderr, `usage: agentprobe [-sess N] [-project N] [-thinking] "message"`)
		os.Exit(2)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(err)
	}
	if cfg.Token == "" {
		fatal(fmt.Errorf("no token in %s (log in with the TUI first)", cfg.Path()))
	}
	client := apiclient.New(cfg.ServerURL, cfg.Token, types.InterfaceTUI)
	ctx := context.Background()

	id := *sessID
	if id == 0 {
		sess, err := client.CreateSession(ctx, "probe", "")
		if err != nil {
			fatal(err)
		}
		id = sess.ID
		if *projectID != 0 {
			// A project session sees the project's documents/memory: create the
			// personal session, then move it into the project.
			id, err = client.AssignSession(ctx, *projectID, id)
			if err != nil {
				fatal(err)
			}
		}
	}
	if *projectID != 0 {
		fmt.Printf("== project %d session #%d on %s ==\n> %s\n", *projectID, id, cfg.ServerURL, msg)
	} else {
		fmt.Printf("== session #%d on %s ==\n> %s\n", id, cfg.ServerURL, msg)
	}

	// Cold resume (have_seq 0) and submit the prompt over the live session socket.
	var sess *apiclient.Session
	if *projectID != 0 {
		sess, err = client.OpenProjectSession(ctx, *projectID, id, 0)
	} else {
		sess, err = client.OpenSession(ctx, id, 0)
	}
	if err != nil {
		fatal(err)
	}
	defer sess.Close()
	if err := sess.Submit(msg); err != nil {
		fatal(err)
	}

	var final strings.Builder
	step := 0
	for ev := range sess.Events() {
		switch ev.Type {
		case types.SSEToolCall:
			step++
			fmt.Printf("\n[tool %d] %s  %s\n", step, ev.ToolName, oneLine(ev.ToolArgs, 700))
		case types.SSEToolResult:
			fmt.Printf("   => (%dms) %s\n", ev.DurationMS, oneLine(ev.Output, 900))
		case types.SSEThinking:
			if *showThinking {
				fmt.Print(ev.Thinking)
			}
		case types.SSEToken:
			final.WriteString(ev.Text)
		case types.SSEAskUser:
			fmt.Printf("\n[ask_user] %s  options=%v\n", ev.Text, ev.Options)
		case types.SSEError:
			fmt.Printf("\n[ERROR] %s\n", ev.Error)
		case types.SSEDone:
			fmt.Printf("\n[done] model=%s ctx=%d/%d\n", ev.Model, ev.ContextTokens, ev.ContextMax)
			if s := strings.TrimSpace(final.String()); s != "" {
				fmt.Printf("\n=== final answer ===\n%s\n", s)
			}
			return
		}
	}
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
