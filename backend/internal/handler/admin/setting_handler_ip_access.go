package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// ipAccessControlUpdateRequest 指针字段区分"未提供"（保持现值）与显式设置。
type ipAccessControlUpdateRequest struct {
	IPBlacklistEnabled *bool     `json:"ip_blacklist_enabled"`
	IPBlacklist        *[]string `json:"ip_blacklist"`
	IPBlacklistMessage *string   `json:"ip_blacklist_message"`
	IPv6BlockEnabled   *bool     `json:"ipv6_block_enabled"`
	IPv6BlockMessage   *string   `json:"ipv6_block_message"`
	// Force 跳过"当前管理员 IP 会被黑名单拦截"的自锁保护。
	Force bool `json:"force"`
}

// GetIPAccessControl 读取 IP 访问控制配置
// GET /api/v1/admin/risk-control/ip-access-control
func (h *SettingHandler) GetIPAccessControl(c *gin.Context) {
	settings, err := h.settingService.GetIPAccessControlSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

// UpdateIPAccessControl 更新 IP 访问控制配置
// PUT /api/v1/admin/risk-control/ip-access-control
func (h *SettingHandler) UpdateIPAccessControl(c *gin.Context) {
	var req ipAccessControlUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	settings, err := h.settingService.GetIPAccessControlSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if req.IPBlacklistEnabled != nil {
		settings.IPBlacklistEnabled = *req.IPBlacklistEnabled
	}
	if req.IPBlacklist != nil {
		settings.IPBlacklist = *req.IPBlacklist
	}
	if req.IPBlacklistMessage != nil {
		settings.IPBlacklistMessage = *req.IPBlacklistMessage
	}
	if req.IPv6BlockEnabled != nil {
		settings.IPv6BlockEnabled = *req.IPv6BlockEnabled
	}
	if req.IPv6BlockMessage != nil {
		settings.IPv6BlockMessage = *req.IPv6BlockMessage
	}

	// 自锁保护：黑名单启用且命中当前管理员 IP 时拒绝保存，避免管理员把自己锁在门外。
	if settings.IPBlacklistEnabled && !req.Force {
		clientIP := middleware2.SecurityClientIP(c)
		if clientIP != "" && ip.MatchesCompiledRules(clientIP, ip.CompileIPRules(settings.IPBlacklist)) {
			response.BadRequest(c, "current admin IP "+clientIP+" would be blocked by this blacklist; pass force=true to save anyway")
			return
		}
	}

	updated, err := h.settingService.SetIPAccessControlSettings(c.Request.Context(), settings)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, updated)
}
