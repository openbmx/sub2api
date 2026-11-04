package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesClaudeThinkingUsesModelCapabilities(t *testing.T) {
	for _, model := range []string{"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-5", "claude-fable-5-1"} {
		t.Run(model, func(t *testing.T) {
			req := &ResponsesRequest{Model: model, Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: "high"}}
			out, err := ResponsesToClaudeRequest(req)
			require.NoError(t, err)
			require.NotNil(t, out.Thinking)
			require.Equal(t, "adaptive", out.Thinking.Type)
			require.Zero(t, out.Thinking.BudgetTokens)
		})
	}
}

func TestResponsesManualThinkingKeepsCallerOutputCap(t *testing.T) {
	for _, effort := range []string{"medium", "high", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			cap := 2048
			req := &ResponsesRequest{Model: "claude-opus-4-5", Input: json.RawMessage(`"hello"`), MaxOutputTokens: &cap, Reasoning: &ResponsesReasoning{Effort: effort}}
			out, err := ResponsesToClaudeRequest(req)
			require.NoError(t, err)
			require.Equal(t, cap, out.MaxTokens)
			require.NotNil(t, out.Thinking)
			require.Less(t, out.Thinking.BudgetTokens, cap)
			require.GreaterOrEqual(t, out.Thinking.BudgetTokens, 1024)
		})
	}
	for _, effort := range []string{"none", "minimal", "disabled"} {
		req := &ResponsesRequest{Model: "claude-opus-4-5", Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: effort}}
		out, err := ResponsesToClaudeRequest(req)
		require.NoError(t, err)
		require.Nil(t, out.Thinking)
		require.Nil(t, out.OutputConfig)
	}
	cap := 1024
	_, err := ResponsesToClaudeRequest(&ResponsesRequest{Model: "claude-opus-4-5", Input: json.RawMessage(`"hello"`), MaxOutputTokens: &cap, Reasoning: &ResponsesReasoning{Effort: "high"}})
	require.Error(t, err)
	unknown, err := ResponsesToClaudeRequest(&ResponsesRequest{Model: "third-party-model", Input: json.RawMessage(`"hello"`), Reasoning: &ResponsesReasoning{Effort: "high"}})
	require.NoError(t, err)
	require.Equal(t, 10240, unknown.Thinking.BudgetTokens)
}

func TestGenericAnthropicBridgePreservesAntigravityClaudeBudget(t *testing.T) {
	cap := 8192
	out, err := ResponsesToAnthropicRequest(&ResponsesRequest{Model: "claude-opus-4-6", Input: json.RawMessage(`"hello"`), MaxOutputTokens: &cap, Reasoning: &ResponsesReasoning{Effort: "medium"}})
	require.NoError(t, err)
	require.Equal(t, "enabled", out.Thinking.Type)
	require.Equal(t, 4096, out.Thinking.BudgetTokens)
	require.Equal(t, cap, out.MaxTokens)
}
