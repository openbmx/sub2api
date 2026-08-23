//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type ipAccessSettingRepoStub struct {
	values map[string]string
}

func (s *ipAccessSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (s *ipAccessSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (s *ipAccessSettingRepoStub) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *ipAccessSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := map[string]string{}
	for _, key := range keys {
		if v, ok := s.values[key]; ok {
			result[key] = v
		}
	}
	return result, nil
}

func (s *ipAccessSettingRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	for k, v := range settings {
		s.values[k] = v
	}
	return nil
}

func (s *ipAccessSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}

func (s *ipAccessSettingRepoStub) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func newIPAccessTestRouter(t *testing.T, settings service.IPAccessControlSettings) *gin.Engine {
	t.Helper()
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	settingService := service.NewSettingService(&ipAccessSettingRepoStub{values: map[string]string{
		"ip_access_control_config": string(raw),
	}}, nil)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(IPAccessControl(settingService))
	ok := func(c *gin.Context) { c.String(http.StatusOK, "ok") }
	router.POST("/v1/messages", ok)
	router.GET("/api/v1/user/profile", ok)
	router.POST("/api/v1/auth/register", ok)
	router.GET("/", ok)
	return router
}

func doIPAccessRequest(router *gin.Engine, method, path, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = remoteAddr
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestIPAccessControl_BlacklistBlocksEverywhereWithCustomMessage(t *testing.T) {
	router := newIPAccessTestRouter(t, service.IPAccessControlSettings{
		IPBlacklistEnabled: true,
		IPBlacklist:        []string{"9.9.9.9", "172.30.0.0/16"},
		IPBlacklistMessage: "你已被封禁",
	})

	// 网关路径：网关错误结构
	recorder := doIPAccessRequest(router, http.MethodPost, "/v1/messages", "9.9.9.9:33333")
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "你已被封禁")
	require.Contains(t, recorder.Body.String(), "ACCESS_DENIED")

	// 面板路径：统一 response 结构
	recorder = doIPAccessRequest(router, http.MethodGet, "/api/v1/user/profile", "172.30.5.6:2222")
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "你已被封禁")

	// SPA/根路径同样拦截
	recorder = doIPAccessRequest(router, http.MethodGet, "/", "9.9.9.9:1000")
	require.Equal(t, http.StatusForbidden, recorder.Code)

	// 非黑名单 IP 放行
	recorder = doIPAccessRequest(router, http.MethodPost, "/v1/messages", "8.8.8.8:1000")
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestIPAccessControl_BlacklistDefaultMessage(t *testing.T) {
	router := newIPAccessTestRouter(t, service.IPAccessControlSettings{
		IPBlacklistEnabled: true,
		IPBlacklist:        []string{"9.9.9.9"},
	})
	recorder := doIPAccessRequest(router, http.MethodPost, "/v1/messages", "9.9.9.9:1000")
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), service.DefaultIPBlacklistMessage)
}

func TestIPAccessControl_IPv6BlockOnlyAffectsGatewayPaths(t *testing.T) {
	router := newIPAccessTestRouter(t, service.IPAccessControlSettings{
		IPv6BlockEnabled: true,
		IPv6BlockMessage: "IPv6 禁止调用",
	})

	// 公网 IPv6 调用网关 → 403 + 自定义提示
	recorder := doIPAccessRequest(router, http.MethodPost, "/v1/messages", "[2400:8902::1]:44444")
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "IPv6 禁止调用")

	// 公网 IPv6 访问面板 → 放行（仅注册被服务层拦截）
	recorder = doIPAccessRequest(router, http.MethodGet, "/api/v1/user/profile", "[2400:8902::1]:44444")
	require.Equal(t, http.StatusOK, recorder.Code)

	// IPv4 调用网关 → 放行
	recorder = doIPAccessRequest(router, http.MethodPost, "/v1/messages", "1.2.3.4:1000")
	require.Equal(t, http.StatusOK, recorder.Code)

	// 环回 IPv6 调用网关 → 放行（不拦内网流量）
	recorder = doIPAccessRequest(router, http.MethodPost, "/v1/messages", "[::1]:1000")
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestIPAccessControl_DisabledPassesThrough(t *testing.T) {
	router := newIPAccessTestRouter(t, service.IPAccessControlSettings{
		IPBlacklist: []string{"9.9.9.9"},
	})
	recorder := doIPAccessRequest(router, http.MethodPost, "/v1/messages", "9.9.9.9:1000")
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestIPAccessControl_NilSettingServicePassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(IPAccessControl(nil))
	router.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	recorder := doIPAccessRequest(router, http.MethodGet, "/", "9.9.9.9:1000")
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestIsIPv6RestrictedPath(t *testing.T) {
	restricted := []string{
		"/v1/messages", "/v1beta/models/gemini-pro", "/backend-api/codex/responses",
		"/antigravity/v1/messages", "/responses", "/messages/count_tokens",
		"/chat/completions", "/embeddings", "/models", "/alpha/search",
		"/images/generations", "/videos/generations", "/tts", "/stt", "/realtime",
		"/web_search", "/x_search",
	}
	for _, path := range restricted {
		require.True(t, isIPv6RestrictedPath(path), path)
	}
	allowed := []string{"/api/v1/auth/login", "/api/v1/user/profile", "/", "/health", "/setup/status", "/dashboard"}
	for _, path := range allowed {
		require.False(t, isIPv6RestrictedPath(path), path)
	}
}
