package middleware

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ipv6RestrictedPathPrefixes 网关 API 前缀集合：IPv6 拦截只作用于这些路径
// （面板 /api/ 与 SPA 静态资源不受影响；注册链路由 IsRegistrationEnabled 统一拦截）。
// 与 internal/web/embed_on.go 的后端端点前缀及 routes/gateway.go 的根别名保持同步。
var ipv6RestrictedPathPrefixes = []string{
	"/v1/",
	"/v1beta/",
	"/backend-api/",
	"/antigravity/",
	"/responses",
	"/messages",
	"/chat/",
	"/embeddings",
	"/models",
	"/alpha/",
	"/images/",
	"/videos/",
	"/tts",
	"/stt",
	"/custom-voices",
	"/realtime",
	"/web_search",
	"/x_search",
}

func isIPv6RestrictedPath(path string) bool {
	for _, prefix := range ipv6RestrictedPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// abortIPAccessDenied 按请求面选择 403 响应形态：
// 面板 /api/ 走统一 response 结构，网关及其他路径走网关错误结构。
func abortIPAccessDenied(c *gin.Context, message string) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		response.Forbidden(c, message)
		c.Abort()
		return
	}
	AbortWithError(c, 403, "ACCESS_DENIED", message)
}

// IPAccessControl 全局 IP 访问控制中间件（风控中心配置）：
//  1. IP/IP 段黑名单：命中即对所有入口（面板、网关、静态资源）返回 403 + 自定义提示语；
//  2. IPv6 拦截：开启后公网 IPv6 客户端调用网关 API 返回 403 + 自定义提示语
//     （注册链路在 SettingService.IsRegistrationEnabled 内统一拦截）。
//
// 必须注册在 SessionBindingContext 之后（依赖其注入的安全客户端 IP）。
// 配置读取走进程内缓存（约 5s 收敛），设置服务不可用时放行。
func IPAccessControl(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settingService == nil {
			c.Next()
			return
		}
		runtime := settingService.GetIPAccessControlRuntimeCached(c.Request.Context())
		if !runtime.Enabled() {
			c.Next()
			return
		}

		clientIP := SecurityClientIP(c)
		if runtime.BlacklistMatches(clientIP) {
			MarkIngressRejected(c, IngressRejectIPRestricted)
			message := runtime.Settings.IPBlacklistMessage
			if message == "" {
				message = service.DefaultIPBlacklistMessage
			}
			abortIPAccessDenied(c, message)
			return
		}

		if runtime.Settings.IPv6BlockEnabled && isIPv6RestrictedPath(c.Request.URL.Path) && runtime.IPv6Blocked(clientIP) {
			MarkIngressRejected(c, IngressRejectIPRestricted)
			message := runtime.Settings.IPv6BlockMessage
			if message == "" {
				message = service.DefaultIPv6BlockMessage
			}
			abortIPAccessDenied(c, message)
			return
		}

		c.Next()
	}
}
