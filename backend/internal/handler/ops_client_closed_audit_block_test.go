package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 提示词审计的拦截响应码允许配成 499。shouldSkipOpsClientClosed 把「499 且无上游
// 错误上下文」当成客户端取消丢弃，而拦截行永远没有上游上下文——不豁免的话，
// 拦截记录会从 ops 错误日志里整行消失。
func TestShouldSkipOpsClientClosedKeepsSecurityAuditBlocks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.True(t, ops.OpsAdvancedSettingsSnapshot().IgnoreContextCanceled, "precondition: defaults drop pure client cancels")

	cancelled, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.True(t, shouldSkipOpsClientClosed(cancelled, ops, statusClientClosedRequest), "precondition: a pure client cancel is skipped")

	blocked, _ := gin.CreateTestContext(httptest.NewRecorder())
	markSecurityAuditBlock(blocked, securityaudit.Decision{Kind: securityaudit.DecisionBlock})
	require.False(t, shouldSkipOpsClientClosed(blocked, ops, statusClientClosedRequest), "an audit block answered with 499 must still be logged")
}
