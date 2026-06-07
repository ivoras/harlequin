// Command agentprobe drives the Harlequin agent like a real client: it reads the
// saved token/server URL from client.yaml (the same file the TUI uses), opens a
// conversation, sends a message, and prints a compact trace of the agent's tool
// calls and results. It exists to debug what the (small) model actually does.
//
//	go run ./cmd/agentprobe [-conv N] [-thinking] "your message"
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
	convID := flag.Int64("conv", 0, "reuse an existing conversation id (0 = create new)")
	showThinking := flag.Bool("thinking", false, "print model thinking")
	flag.Parse()
	msg := strings.Join(flag.Args(), " ")
	if msg == "" {
		fmt.Fprintln(os.Stderr, `usage: agentprobe [-conv N] [-thinking] "message"`)
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

	id := *convID
	if id == 0 {
		conv, err := client.CreateConversation(ctx, "probe", "")
		if err != nil {
			fatal(err)
		}
		id = conv.ID
	}
	fmt.Printf("== conversation #%d on %s ==\n> %s\n", id, cfg.ServerURL, msg)

	var final strings.Builder
	step := 0
	err = client.SendMessage(ctx, id, msg, func(ev types.StreamEvent) {
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
		}
	})
	if err != nil {
		fatal(err)
	}
	if s := strings.TrimSpace(final.String()); s != "" {
		fmt.Printf("\n=== final answer ===\n%s\n", s)
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
