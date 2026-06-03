package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ivoras/harlequin/internal/server/mcp"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
)

// mcpToolPrefix namespaces external MCP tools as mcp__<server>__<tool>, matching
// the convention models are familiar with from hosted connectors.
const mcpToolPrefix = "mcp__"

// mcpToolListTimeout bounds per-server tool discovery during a turn, so an
// unreachable MCP server can't stall the agent.
const mcpToolListTimeout = 6 * time.Second

// registerMCPTools adds the tools advertised by every enabled, visible (shared +
// user) MCP server whose auth is satisfied. Servers needing OAuth that the user
// hasn't authorized are skipped silently (their status is shown via /mcp).
// Failures never break the turn.
func (a *Agent) registerMCPTools(ctx context.Context, rc *runContext, reg map[string]toolEntry) {
	servers, err := a.MCP.Registry().ListVisible(ctx, rc.userDB)
	if err != nil {
		log.Printf("mcp: list servers: %v", err)
		return
	}
	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		// Bound each server's tool discovery so one unreachable server can't
		// stall the whole turn.
		listCtx, cancel := context.WithTimeout(ctx, mcpToolListTimeout)
		tools, err := a.MCP.Tools(listCtx, rc.userID, rc.userDB, srv)
		cancel()
		if err != nil {
			if !errors.Is(err, mcp.ErrNeedAuth) {
				log.Printf("mcp: list tools for %s/%s: %v", srv.Scope, srv.Name, err)
			}
			continue
		}
		srv := srv // capture per server
		for _, t := range tools {
			full := mcpToolPrefix + sanitizeToolName(srv.Name) + "__" + t.Name
			if _, exists := reg[full]; exists {
				continue // name clash (e.g. shared+user same name): first wins
			}
			params := t.InputSchema
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			toolName := t.Name
			reg[full] = toolEntry{
				def: fnTool(full, t.Description, params),
				handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
					return a.callMCPTool(ctx, rc, srv, toolName, full, args)
				},
			}
		}
	}
}

func (a *Agent) callMCPTool(ctx context.Context, rc *runContext, srv mcp.Server, tool, full string, args map[string]any) (string, error) {
	argStr := truncateArgs(marshalArgs(args), 200)
	start := time.Now()
	out, err := a.MCP.Call(ctx, rc.userID, rc.userDB, srv, tool, args)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		a.logEvent(ctx, rc, sessionlog.TypeMCPCall, map[string]any{
			"scope": srv.Scope, "server": srv.Name, "tool": tool, "ok": false,
			"duration_ms": ms, "error": err.Error(), "args": argStr,
		})
		log.Printf("mcp: call %s/%s.%s failed after %dms (args: %s): %v", srv.Scope, srv.Name, tool, ms, argStr, err)
		return fmt.Sprintf("error: MCP tool %s failed: %v", full, err), nil
	}
	a.logEvent(ctx, rc, sessionlog.TypeMCPCall, map[string]any{
		"scope": srv.Scope, "server": srv.Name, "tool": tool, "ok": true,
		"duration_ms": ms, "bytes": len(out), "args": argStr,
	})
	log.Printf("mcp: call %s/%s.%s (%dms, %d bytes, args: %s)", srv.Scope, srv.Name, tool, ms, len(out), argStr)
	return out, nil
}

func marshalArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// sanitizeToolName keeps tool names within the [A-Za-z0-9_-] character class that
// providers accept, replacing anything else with an underscore.
func sanitizeToolName(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	return string(b)
}
