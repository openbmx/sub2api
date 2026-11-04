package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
)

const GatewayFailureReasonUpstreamRetryDisallowed GatewayFailureReason = "upstream_retry_disallowed"

func WriteStoppedUpstreamResponseHeaders(dst, src http.Header) {
	for name, values := range src {
		switch strings.ToLower(name) {
		case "content-type", "content-encoding", "content-length", "transfer-encoding", "connection":
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

// An explicit upstream stop applies to this request, including credential failover.
func (s *GatewayService) claudeStoppedUpstreamError(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, model string) *UpstreamFailoverError {
	if account.Platform != PlatformAnthropic || resp.StatusCode < 400 ||
		!strings.EqualFold(strings.TrimSpace(resp.Header.Get("x-should-retry")), "false") {
		return nil
	}
	body, _ := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	message := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(body))
	if message == "" {
		message = "Upstream request rejected"
	}
	if s.rateLimitService != nil {
		s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, model)
	}
	setOpsUpstreamError(c, resp.StatusCode, message, "")
	return &UpstreamFailoverError{
		StatusCode: resp.StatusCode, ResponseBody: body,
		ResponseHeaders:   responseheaders.FilterHeaders(resp.Header, s.responseHeaderFilter),
		NextAccountAction: NextAccountStop, ClientStatusCode: resp.StatusCode, ClientMessage: message,
		Reason: GatewayFailureReasonUpstreamRetryDisallowed,
	}
}
