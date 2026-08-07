package securityaudit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tool calls, tool results and reasoning blocks are fully client-controlled, so
// a payload placed there must reach the auditor. Before this coverage existed a
// request could smuggle content past moderation by putting it in a tool_result.
func TestExtractionCoversClientControlledToolBlocks(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		body     string
		want     []string
	}{
		{
			name:     "anthropic tool_use, tool_result and thinking",
			protocol: "anthropic_messages",
			body: `{"messages":[
				{"role":"user","content":[{"type":"text","text":"PLAIN"}]},
				{"role":"assistant","content":[
					{"type":"thinking","thinking":"THINKING"},
					{"type":"tool_use","id":"t1","name":"bash","input":{"command":"TOOL_INPUT"}}
				]},
				{"role":"user","content":[
					{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"TOOL_RESULT"}]}
				]}
			]}`,
			want: []string{"PLAIN", "THINKING", "TOOL_INPUT", "TOOL_RESULT", "bash"},
		},
		{
			name:     "anthropic tool_result with a bare string body",
			protocol: "anthropic_messages",
			body: `{"messages":[
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"STRING_RESULT"}]}
			]}`,
			want: []string{"STRING_RESULT"},
		},
		{
			name:     "openai chat tool_calls and tool role output",
			protocol: "openai_chat_completions",
			body: `{"messages":[
				{"role":"assistant","content":null,"tool_calls":[
					{"id":"c1","type":"function","function":{"name":"run","arguments":"{\"cmd\":\"OPENAI_ARGS\"}"}}
				]},
				{"role":"tool","tool_call_id":"c1","content":"OPENAI_TOOL_OUTPUT"}
			]}`,
			want: []string{"OPENAI_ARGS", "OPENAI_TOOL_OUTPUT", "run"},
		},
		{
			name:     "openai responses function_call and output",
			protocol: "openai_responses",
			body: `{"input":[
				{"type":"function_call","name":"run","arguments":"{\"cmd\":\"RESPONSES_ARGS\"}"},
				{"type":"function_call_output","call_id":"c1","output":"RESPONSES_OUTPUT"}
			]}`,
			want: []string{"RESPONSES_ARGS", "RESPONSES_OUTPUT"},
		},
		{
			name:     "gemini functionCall and functionResponse",
			protocol: "gemini",
			body: `{"contents":[
				{"role":"model","parts":[{"functionCall":{"name":"run","args":{"cmd":"GEMINI_ARGS"}}}]},
				{"role":"user","parts":[{"functionResponse":{"name":"run","response":{"out":"GEMINI_RESPONSE"}}}]}
			]}`,
			want: []string{"GEMINI_ARGS", "GEMINI_RESPONSE"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := ExtractPromptSnapshot(Request{Protocol: tc.protocol, Body: []byte(tc.body), Stage: "http"})
			require.NoError(t, err)
			for _, canary := range tc.want {
				require.Contains(t, snapshot.ScanText, canary,
					"client-controlled content must be auditable, not silently dropped")
			}
		})
	}
}

// Blocking mode may narrow the snapshot to the latest turn; a tool result in
// that turn must still be scanned.
func TestBlockingSnapshotKeepsLatestTurnToolResult(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"OLD"}]},
		{"role":"assistant","content":[{"type":"text","text":"PRIOR_OUTPUT"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"LATEST_TOOL_RESULT"}]}
	]}`
	snapshot, err := ExtractBlockingPromptSnapshot(Request{Protocol: "anthropic_messages", Body: []byte(body), Stage: "http"}, true)
	require.NoError(t, err)
	require.Contains(t, snapshot.ScanText, "LATEST_TOOL_RESULT")
}

// The extractor walks attacker-supplied JSON, so nesting must not drive
// unbounded recursion — and cutting recursion off must not open a bypass.
// Past the depth bound the remaining subtree is serialized as JSON instead of
// walked, so deeply buried text is still scanned.
func TestDeeplyNestedToolResultIsBoundedButStillAudited(t *testing.T) {
	payload := map[string]any{"type": "tool_result", "content": "DEEP_CANARY"}
	for i := 0; i < 200; i++ {
		payload = map[string]any{"type": "tool_result", "content": []any{payload}}
	}
	encoded, err := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": []any{payload}}},
	})
	require.NoError(t, err)

	snapshot, err := ExtractPromptSnapshot(Request{Protocol: "anthropic_messages", Body: encoded, Stage: "http"})
	require.NoError(t, err)
	require.Contains(t, snapshot.ScanText, "DEEP_CANARY",
		"burying content past the recursion bound must not hide it from the auditor")
}

func TestEncodeStructuredValueSkipsEmptyPayloads(t *testing.T) {
	for _, empty := range []any{nil, "", "   ", map[string]any{}, []any{}} {
		require.Equal(t, "", encodeStructuredValue(empty))
	}
	require.Equal(t, `{"a":1}`, encodeStructuredValue(map[string]any{"a": 1}))
}

// A tool call with neither name nor arguments contributes nothing, so it must
// not inject an empty segment that inflates the message count.
func TestToolCallWithoutPayloadProducesNoSegment(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"KEEP"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]}
	]}`
	snapshot, err := ExtractPromptSnapshot(Request{Protocol: "anthropic_messages", Body: []byte(body), Stage: "http"})
	require.NoError(t, err)
	require.Equal(t, 1, snapshot.MessageCount)
	require.Equal(t, "KEEP", strings.TrimSpace(snapshot.ScanText))
}
