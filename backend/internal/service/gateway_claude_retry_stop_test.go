//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type claudeStoppedUpstream struct {
	calls  int
	status int
}

func (u *claudeStoppedUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: u.status, Header: http.Header{"Content-Type": {"application/json"}, "X-Should-Retry": {"false"}}, Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"permission_error","message":"Request explicitly rejected"}}`))}, nil
}
func (u *claudeStoppedUpstream) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}

func TestClaudeExplicitNoRetryStopsEveryGatewayRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"messages", "passthrough", "responses", "chat", "count"} {
		t.Run(route, func(t *testing.T) {
			upstream := &claudeStoppedUpstream{status: http.StatusForbidden}
			svc := claudeRepairOAuthService(t, &anthropicHTTPUpstreamRecorder{}, true)
			svc.httpUpstream = upstream
			svc.rateLimitService = &RateLimitService{accountRepo: &rateLimitAccountRepoStub{}, cfg: &config.Config{}}
			account := newAnthropicOAuthAccountForPartialUsageTest()
			raw := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)
			if route == "responses" {
				raw = []byte(`{"model":"claude-sonnet-4-6","input":"hello"}`)
			}
			if route == "passthrough" {
				account = newAnthropicAPIKeyAccountForTest()
			}
			c, rec := claudeRepairContext(raw, "test-client/1.0")
			var err error
			switch route {
			case "responses":
				_, err = svc.ForwardAsResponses(context.Background(), c, account, raw, nil)
			case "chat":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, raw, nil)
			case "count":
				err = svc.ForwardCountTokens(context.Background(), c, account, claudeRepairParse(t, raw))
			default:
				_, err = svc.Forward(context.Background(), c, account, claudeRepairParse(t, raw))
			}
			require.Error(t, err)
			require.Equal(t, 1, upstream.calls)
			if route == "count" {
				require.Equal(t, http.StatusForbidden, rec.Code)
				require.Equal(t, "false", rec.Header().Get("x-should-retry"))
			} else {
				var stop *UpstreamFailoverError
				require.True(t, errors.As(err, &stop))
				require.False(t, stop.ShouldRetryNextAccount())
				require.Equal(t, http.StatusForbidden, stop.ClientStatusCode)
				require.Equal(t, "false", stop.ResponseHeaders.Get("x-should-retry"))
			}
		})
	}
}

type claudeRectifierStopUpstream struct {
	anthropicHTTPUpstreamRecorder
	calls, stopAt int
}

func (u *claudeRectifierStopUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	header := http.Header{"Content-Type": {"application/json"}}
	if u.calls >= u.stopAt {
		header.Set("x-should-retry", "false")
	}
	return &http.Response{StatusCode: 400, Header: header, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"tool_use signature invalid"}}`))}, nil
}

func TestClaudeRectifierResponsesAlsoHonorStop(t *testing.T) {
	for _, count := range []bool{false, true} {
		for _, stopAt := range []int{2, 3} {
			if count && stopAt == 3 {
				continue
			}
			upstream := &claudeRectifierStopUpstream{stopAt: stopAt}
			svc := claudeRepairOAuthService(t, &anthropicHTTPUpstreamRecorder{}, true)
			svc.httpUpstream = upstream
			body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":2048,"messages":[{"role":"user","content":"hello"}]}`)
			c, rec := claudeRepairContext(body, "test-client/1.0")
			account := newAnthropicOAuthAccountForPartialUsageTest()
			var err error
			if count {
				err = svc.ForwardCountTokens(context.Background(), c, account, claudeRepairParse(t, body))
			} else {
				_, err = svc.Forward(context.Background(), c, account, claudeRepairParse(t, body))
			}
			var stopped *UpstreamFailoverError
			require.ErrorAs(t, err, &stopped)
			require.False(t, stopped.ShouldRetryNextAccount())
			require.Equal(t, stopAt, upstream.calls)
			if count {
				require.Equal(t, 400, rec.Code)
				require.Equal(t, "false", rec.Header().Get("x-should-retry"))
			}
		}
	}
}
