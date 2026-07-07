package agent

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// ContextBreakdown estimates how a session's next request would fill the
// model's context window, broken down into system prompt, skill catalogue,
// tool definitions and conversation history, without calling the LLM. model,
// if non-empty, is used to resolve the context window size (the caller
// typically has this from the last SSEDone event); otherwise the provider's
// default window size is used.
func (a *Agent) ContextBreakdown(ctx context.Context, projectID, sessionID, userID int64, username, role, model string) (*types.ContextBreakdown, error) {
	rc := &runContext{
		sessionID: sessionID, userID: userID, username: username,
		canShareMemory: types.IsElevated(role), projectID: projectID, turn: 1,
	}

	var result *types.ContextBreakdown
	compute := func() error {
		a.loadHat(ctx, rc)
		rc.skillInfos, _ = a.Skills.EffectiveSkillInfos(ctx, rc.userDB, rc.projectDB, rc.hat)

		tools := a.buildTools(ctx, rc)
		toolDefs := make([]llm.Tool, 0, len(tools))
		for _, t := range tools {
			toolDefs = append(toolDefs, t.def)
		}
		var toolsTokens int
		for _, t := range toolDefs {
			toolsTokens += llm.EstimateToolTokens(t)
		}

		base := a.basePrompt(rc)
		systemPromptTokens := llm.EstimateTextTokens(base)
		if rc.hat != nil {
			systemPromptTokens += llm.EstimateTextTokens(fmt.Sprintf("\n\nYou are wearing the %q hat.", rc.hat.Name))
		}

		var skillsTokens int
		if len(rc.skillInfos) > 0 {
			skillsText := "\n\nAvailable skills (use load_skill to read full instructions):\n"
			for _, i := range rc.skillInfos {
				skillsText += fmt.Sprintf("- %s: %s\n", i.Name, i.Description)
			}
			skillsTokens = llm.EstimateTextTokens(skillsText)
		}

		history, err := a.Sessions.Messages(ctx, rc.sessDB, sessionID)
		if err != nil {
			return err
		}
		var messagesTokens int
		for _, m := range history {
			messagesTokens += llm.EstimateMessageTokens(llm.Message{
				Role: m.Role, Content: m.Content, ToolCalls: toLLMToolCalls(m.ToolCalls),
				ToolCallID: m.ToolCallID, Name: m.Name,
			})
		}

		total := systemPromptTokens + skillsTokens + toolsTokens + messagesTokens
		contextMax := 0
		if a.ContextMax != nil {
			contextMax = a.ContextMax(model)
		}
		result = &types.ContextBreakdown{
			Model:      model,
			ContextMax: contextMax,
			Total:      total,
			Categories: []types.ContextCategory{
				{Name: "System prompt", Tokens: systemPromptTokens},
				{Name: "Skills catalogue", Tokens: skillsTokens},
				{Name: "Tools", Tokens: toolsTokens},
				{Name: "Messages", Tokens: messagesTokens},
			},
		}
		return nil
	}

	run := func(userDB *sql.DB) error {
		rc.userDB = userDB
		rc.sessDB = userDB
		if projectID > 0 {
			return a.Storage.WithProjectReadOnly(ctx, projectID, func(projDB *sql.DB) error {
				rc.sessDB = projDB
				rc.projectDB = projDB
				return compute()
			})
		}
		return compute()
	}
	if err := a.Storage.WithUserReadOnly(ctx, userID, run); err != nil {
		return nil, err
	}
	return result, nil
}
