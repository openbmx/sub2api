//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeToolReferencesRoundTripWithoutChangingPayload(t *testing.T) {
	body := []byte(`{"tools":[{"name":"sessions_list","defer_loading":true,"description":"literal sessions_list","input_schema":{"type":"object"}}],"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"sessions_list"}},{"type":"tool_removal","tool":{"type":"tool_reference","name":"sessions_list"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_literal","content":[{"type":"tool_reference","tool_name":"sessions_list"},{"type":"text","text":"literal sessions_list"}]},{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_literal","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"sessions_list"}]}},{"type":"tool_use","id":"toolu_sessions_list","name":"sessions_list","input":{"name":"sessions_list"}}]}]}`)
	rw := buildToolNameRewriteFromBody(body)
	require.NotNil(t, rw)
	out := applyToolNameRewriteToBody(body, rw)
	for _, path := range []string{"tools.0.name", "messages.0.content.0.tool.name", "messages.0.content.1.tool.name", "messages.1.content.0.content.0.tool_name", "messages.1.content.1.content.tool_references.0.tool_name", "messages.1.content.2.name"} {
		require.Equal(t, "cc_sess_list", gjson.GetBytes(out, path).String(), path)
	}
	require.Equal(t, "sessions_list", gjson.GetBytes(out, "messages.1.content.2.input.name").String())
	require.Equal(t, "toolu_sessions_list", gjson.GetBytes(out, "messages.1.content.2.id").String())
	require.Equal(t, "literal sessions_list", gjson.GetBytes(out, "tools.0.description").String())
	restored := restoreToolNamesInBytes(out, rw)
	for _, path := range []string{"messages.0.content.0.tool.name", "messages.0.content.1.tool.name", "messages.1.content.0.content.0.tool_name", "messages.1.content.1.content.tool_references.0.tool_name", "messages.1.content.2.name"} {
		require.Equal(t, "sessions_list", gjson.GetBytes(restored, path).String(), path)
	}
}

func TestClaudeInlineToolDefinitionsUseTheSameMapping(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"sessions_inline","input_schema":{"type":"object"}}}}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_inline","name":"sessions_inline","input":{}}]}]}`)
	rw := buildToolNameRewriteFromBody(body)
	require.NotNil(t, rw)
	out := applyToolNameRewriteToBody(body, rw)
	require.Equal(t, "cc_sess_inline", gjson.GetBytes(out, "messages.0.content.0.tool.definition.name").String())
	require.Equal(t, "cc_sess_inline", gjson.GetBytes(out, "messages.1.content.0.name").String())
	require.JSONEq(t, string(body), string(restoreToolNamesInBytes(out, rw)))
}

func TestClaudeToolChangeExistingToolNameShapeRoundTrips(t *testing.T) {
	body := []byte(`{"tools":[{"name":"sessions_list"}],"messages":[{"role":"user","content":[{"type":"tool_addition","tool":{"type":"tool_reference","tool_name":"sessions_list"}}]}]}`)
	rw := buildToolNameRewriteFromBody(body)
	require.NotNil(t, rw)
	out := applyToolNameRewriteToBody(body, rw)
	require.Equal(t, "cc_sess_list", gjson.GetBytes(out, "messages.0.content.0.tool.tool_name").String())
	restored := restoreToolNamesInBytes(out, rw)
	require.Equal(t, "sessions_list", gjson.GetBytes(restored, "messages.0.content.0.tool.tool_name").String())
}
