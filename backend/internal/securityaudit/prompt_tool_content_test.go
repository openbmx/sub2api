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
		{
			// Codex sends apply_patch as a custom tool; before these types were
			// recognized only the trailing user turn was audited.
			name:     "openai responses custom tool call and output",
			protocol: "openai_responses",
			body: `{"input":[
				{"type":"custom_tool_call","call_id":"c","name":"apply_patch","input":"CUSTOM_INPUT"},
				{"type":"custom_tool_call_output","call_id":"c","output":"CUSTOM_OUTPUT"},
				{"role":"user","content":"继续"}
			]}`,
			want: []string{"CUSTOM_INPUT", "CUSTOM_OUTPUT", "apply_patch"},
		},
		{
			name:     "openai responses mcp call and local shell call",
			protocol: "openai_responses",
			body: `{"input":[
				{"type":"mcp_call","name":"search","arguments":"{\"q\":\"MCP_ARGS\"}","output":"MCP_OUTPUT"},
				{"type":"local_shell_call","call_id":"s","action":{"type":"exec","command":["echo","SHELL_CMD"]}},
				{"type":"local_shell_call_output","call_id":"s","output":"SHELL_OUTPUT"}
			]}`,
			want: []string{"MCP_ARGS", "MCP_OUTPUT", "SHELL_CMD", "SHELL_OUTPUT"},
		},
		{
			name:     "anthropic text document and search result",
			protocol: "anthropic_messages",
			body: `{"messages":[
				{"role":"user","content":[
					{"type":"document","title":"DOC_TITLE","context":"DOC_CONTEXT","source":{"type":"text","media_type":"text/plain","data":"DOC_TEXT"}},
					{"type":"search_result","source":"https://example.com","title":"SR_TITLE","content":[{"type":"text","text":"SR_TEXT"}]}
				]}
			]}`,
			want: []string{"DOC_TITLE", "DOC_CONTEXT", "DOC_TEXT", "SR_TITLE", "SR_TEXT"},
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

// In an OpenAI chat agent loop the request ends in tool traffic, not a user
// turn. Latest-turn mode used to drop everything after the last user message,
// which is exactly where a fetched page or a tool's output lands.
func TestBlockingSnapshotKeepsChatToolTrafficAfterLatestUserTurn(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":"older user input"},
		{"role":"assistant","content":"previous assistant output"},
		{"role":"user","content":"latest user input"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fetch","arguments":"{\"url\":\"TOOL_ARGS\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"FETCHED_PAGE_PAYLOAD"}
	]}`
	snapshot, err := ExtractBlockingPromptSnapshot(Request{Protocol: "openai_chat_completions", Body: []byte(body), Stage: "http"}, true, 0)
	require.NoError(t, err)
	text := snapshot.ScanText
	require.Contains(t, text, "TOOL_ARGS")
	require.Contains(t, text, "FETCHED_PAGE_PAYLOAD")
	require.NotContains(t, text, "older user input")
	// The latest user turn keeps priority; the tool traffic follows it, ahead of
	// the prior assistant output that only exists as context.
	require.Less(t, strings.Index(text, "latest user input"), strings.Index(text, "FETCHED_PAGE_PAYLOAD"))
	require.Less(t, strings.Index(text, "FETCHED_PAGE_PAYLOAD"), strings.Index(text, "previous assistant output"))
}

// A base64 PDF is not text a guard model can judge, and expanding it would put
// megabytes of encoding into every audit call.
func TestDocumentBlockSkipsBinarySources(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[
		{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQKJcfsj6IK"}},
		{"type":"text","text":"SUMMARIZE_THIS"}
	]}]}`
	snapshot, err := ExtractPromptSnapshot(Request{Protocol: "anthropic_messages", Body: []byte(body), Stage: "http"})
	require.NoError(t, err)
	require.NotContains(t, snapshot.ScanText, "JVBERi0x")
	require.Contains(t, snapshot.ScanText, "SUMMARIZE_THIS")
}

// Blocking mode may narrow the snapshot to the latest turn; a tool result in
// that turn must still be scanned.
func TestBlockingSnapshotKeepsLatestTurnToolResult(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":[{"type":"text","text":"OLD"}]},
		{"role":"assistant","content":[{"type":"text","text":"PRIOR_OUTPUT"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"LATEST_TOOL_RESULT"}]}
	]}`
	snapshot, err := ExtractBlockingPromptSnapshot(Request{Protocol: "anthropic_messages", Body: []byte(body), Stage: "http"}, true, 0)
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
