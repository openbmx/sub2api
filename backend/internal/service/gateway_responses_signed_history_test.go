//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeResponsesPreservesSignedToolHistoryAcrossModels(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-6", "claude-opus-4-5"} {
		for _, stream := range []bool{false, true} {
			for _, redacted := range []bool{false, true} {
				name := model + "/buffered"
				if stream {
					name = model + "/stream"
				}
				if redacted {
					name += "/redacted"
				}
				t.Run(name, func(t *testing.T) {
					sse := strings.Join([]string{
						`data: {"type":"message_start","message":{"id":"msg_signed","type":"message","role":"assistant","model":"` + model + `","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
						`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-history"}}`,
						`data: {"type":"content_block_stop","index":0}`,
						`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_lookup","name":"lookup","input":{}}}`,
						`data: {"type":"content_block_stop","index":1}`,
						`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
						`data: {"type":"message_stop"}`,
					}, "\n\n") + "\n\n"
					if redacted {
						sse = strings.Replace(sse, `"type":"thinking","thinking":""`, `"type":"redacted_thinking","data":"redacted-history"`, 1)
						lines := strings.Split(sse, "\n\n")
						sse = strings.Join(append(lines[:2], lines[4:]...), "\n\n")
					}
					var namedEvents []string
					for _, part := range strings.Split(strings.TrimSpace(sse), "\n\n") {
						payload := strings.TrimPrefix(part, "data: ")
						namedEvents = append(namedEvents, "event: "+gjson.Get(payload, "type").String()+"\n"+part)
					}
					sse = strings.Join(namedEvents, "\n\n") + "\n\n"
					upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}}
					svc := newForwardPartialUsageServiceForTest(upstream)
					body, err := json.Marshal(map[string]any{"model": model, "input": "hello", "stream": stream, "reasoning": map[string]any{"effort": "medium"}})
					require.NoError(t, err)
					c, rec := claudeRepairContext(body, "test-client/1.0")
					_, err = svc.ForwardAsResponses(context.Background(), c, newAnthropicAPIKeyAccountForTest(), body, nil)
					require.NoError(t, err)
					response := []byte(rec.Body.String())
					if stream {
						for _, line := range strings.Split(rec.Body.String(), "\n") {
							if strings.HasPrefix(line, "data:") {
								payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
								if gjson.Get(payload, "type").String() == "response.completed" {
									response = []byte(gjson.Get(payload, "response").Raw)
								}
							}
						}
					}
					outputs := gjson.GetBytes(response, "output")
					require.NotEmpty(t, outputs.Get("0.encrypted_content").String())
					var items []apicompat.ResponsesInputItem
					require.NoError(t, json.Unmarshal([]byte(outputs.Raw), &items))
					items = append(items, apicompat.ResponsesInputItem{Type: "function_call_output", CallID: outputs.Get("1.call_id").String(), Output: "found"})
					input, err := json.Marshal(items)
					require.NoError(t, err)
					followup, err := apicompat.ResponsesToClaudeRequest(&apicompat.ResponsesRequest{Model: model, Input: input, Reasoning: &apicompat.ResponsesReasoning{Effort: "medium"}})
					require.NoError(t, err)
					wire, err := json.Marshal(followup)
					require.NoError(t, err)
					if redacted {
						require.Equal(t, "redacted-history", gjson.GetBytes(wire, "messages.0.content.0.data").String())
					} else {
						require.Equal(t, "signed-history", gjson.GetBytes(wire, "messages.0.content.0.signature").String())
					}
				})
			}
		}
	}
}
