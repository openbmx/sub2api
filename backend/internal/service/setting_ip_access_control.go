package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
)

// IPAccessControlSettings 风控中心的 IP 访问控制配置。
// 黑名单命中或 IPv6 拦截命中的请求都会收到 403 与可自定义的提示语。
type IPAccessControlSettings struct {
	// IPBlacklistEnabled IP/IP 段黑名单总开关。
	IPBlacklistEnabled bool `json:"ip_blacklist_enabled"`
	// IPBlacklist 黑名单条目，支持单个 IP（IPv4/IPv6）与 CIDR 段。
	IPBlacklist []string `json:"ip_blacklist"`
	// IPBlacklistMessage 黑名单命中时返回的自定义提示语（403 响应 message）。
	IPBlacklistMessage string `json:"ip_blacklist_message"`
	// IPv6BlockEnabled IPv6 拦截开关：开启后公网 IPv6 无法注册，调用网关 API 返回 403。
	IPv6BlockEnabled bool `json:"ipv6_block_enabled"`
	// IPv6BlockMessage IPv6 拦截命中时返回的自定义提示语（403 响应 message）。
	IPv6BlockMessage string `json:"ipv6_block_message"`
}

// IPAccessControlRuntime 预编译后的运行时视图，供全局中间件在热路径上使用。
type IPAccessControlRuntime struct {
	Settings  IPAccessControlSettings
	Blacklist *ip.CompiledIPRules
}

// Enabled 报告是否存在任何需要在请求路径上执行的拦截。
func (rt *IPAccessControlRuntime) Enabled() bool {
	return rt != nil && (rt.Settings.IPBlacklistEnabled || rt.Settings.IPv6BlockEnabled)
}

// BlacklistMatches 报告客户端 IP 是否命中黑名单（未启用或 IP 非法时返回 false）。
func (rt *IPAccessControlRuntime) BlacklistMatches(clientIP string) bool {
	if rt == nil || !rt.Settings.IPBlacklistEnabled {
		return false
	}
	return ip.MatchesCompiledRules(clientIP, rt.Blacklist)
}

// IPv6Blocked 报告客户端 IP 是否应被 IPv6 拦截（仅公网 IPv6 生效）。
func (rt *IPAccessControlRuntime) IPv6Blocked(clientIP string) bool {
	if rt == nil || !rt.Settings.IPv6BlockEnabled {
		return false
	}
	return ip.IsPublicIPv6(clientIP)
}

const (
	// DefaultIPBlacklistMessage 黑名单命中时的默认提示语。
	DefaultIPBlacklistMessage = "Access denied: your IP address is blocked"
	// DefaultIPv6BlockMessage IPv6 拦截命中时的默认提示语。
	DefaultIPv6BlockMessage = "Access denied: IPv6 access is not allowed"

	// ipAccessControlMaxEntries 黑名单条目数上限，防止配置无限膨胀拖慢每请求匹配。
	ipAccessControlMaxEntries = 5000
	// ipAccessControlMaxMessageLen 自定义提示语长度上限（字符）。
	ipAccessControlMaxMessageLen = 1000

	ipAccessControlCacheTTL  = 5 * time.Second
	ipAccessControlErrorTTL  = 5 * time.Second
	ipAccessControlDBTimeout = 3 * time.Second
)

type cachedIPAccessControlRuntime struct {
	runtime   *IPAccessControlRuntime
	expiresAt time.Time
}

// DefaultIPAccessControlSettings 返回默认（全部关闭）的配置。
func DefaultIPAccessControlSettings() IPAccessControlSettings {
	return IPAccessControlSettings{IPBlacklist: []string{}}
}

// normalizeIPAccessControlSettings 清理条目并校验格式，返回规范化后的配置。
func normalizeIPAccessControlSettings(settings IPAccessControlSettings) (IPAccessControlSettings, error) {
	cleaned := make([]string, 0, len(settings.IPBlacklist))
	seen := make(map[string]struct{}, len(settings.IPBlacklist))
	for _, entry := range settings.IPBlacklist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		cleaned = append(cleaned, entry)
	}
	if len(cleaned) > ipAccessControlMaxEntries {
		return settings, fmt.Errorf("ip blacklist cannot exceed %d entries", ipAccessControlMaxEntries)
	}
	if invalid := ip.ValidateIPPatterns(cleaned); len(invalid) > 0 {
		const maxShown = 5
		shown := invalid
		if len(shown) > maxShown {
			shown = shown[:maxShown]
		}
		return settings, fmt.Errorf("invalid ip or cidr entries: %s", strings.Join(shown, ", "))
	}
	settings.IPBlacklist = cleaned
	settings.IPBlacklistMessage = strings.TrimSpace(settings.IPBlacklistMessage)
	settings.IPv6BlockMessage = strings.TrimSpace(settings.IPv6BlockMessage)
	if len([]rune(settings.IPBlacklistMessage)) > ipAccessControlMaxMessageLen ||
		len([]rune(settings.IPv6BlockMessage)) > ipAccessControlMaxMessageLen {
		return settings, fmt.Errorf("custom message cannot exceed %d characters", ipAccessControlMaxMessageLen)
	}
	return settings, nil
}

// GetIPAccessControlSettings 读取 IP 访问控制配置（不走缓存，供管理端读取）。
// 配置缺失或损坏时返回默认配置。
func (s *SettingService) GetIPAccessControlSettings(ctx context.Context) (IPAccessControlSettings, error) {
	settings := DefaultIPAccessControlSettings()
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyIPAccessControlConfig)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return settings, nil
		}
		return settings, fmt.Errorf("get ip access control settings: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return settings, nil
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		slog.Warn("[Setting] unmarshal ip_access_control_config failed, falling back to defaults", "error", err)
		return DefaultIPAccessControlSettings(), nil
	}
	if settings.IPBlacklist == nil {
		settings.IPBlacklist = []string{}
	}
	return settings, nil
}

// SetIPAccessControlSettings 校验并持久化 IP 访问控制配置，随后立即刷新运行时缓存，
// 保证管理员保存后本节点即时生效（多节点在缓存 TTL 内收敛）。
func (s *SettingService) SetIPAccessControlSettings(ctx context.Context, settings IPAccessControlSettings) (IPAccessControlSettings, error) {
	normalized, err := normalizeIPAccessControlSettings(settings)
	if err != nil {
		return settings, err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return settings, fmt.Errorf("marshal ip access control settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyIPAccessControlConfig, string(data)); err != nil {
		return settings, fmt.Errorf("save ip access control settings: %w", err)
	}
	s.storeIPAccessControlRuntime(buildIPAccessControlRuntime(normalized), ipAccessControlCacheTTL)
	return normalized, nil
}

func buildIPAccessControlRuntime(settings IPAccessControlSettings) *IPAccessControlRuntime {
	runtime := &IPAccessControlRuntime{Settings: settings}
	if settings.IPBlacklistEnabled && len(settings.IPBlacklist) > 0 {
		runtime.Blacklist = ip.CompileIPRules(settings.IPBlacklist)
	}
	return runtime
}

func (s *SettingService) storeIPAccessControlRuntime(runtime *IPAccessControlRuntime, ttl time.Duration) {
	s.ipAccessControlRuntimeCache.Store(&cachedIPAccessControlRuntime{
		runtime:   runtime,
		expiresAt: time.Now().Add(ttl),
	})
}

// GetIPAccessControlRuntimeCached 返回带进程内缓存的运行时配置。
// 全局中间件每个请求都会调用：命中未过期缓存直接返回；过期后返回旧值并在后台刷新
// （stale-while-revalidate），冷启动时同步加载一次。任何失败都回退到"全部关闭"，
// 保证 IP 访问控制的故障不会拖垮正常流量。
func (s *SettingService) GetIPAccessControlRuntimeCached(ctx context.Context) *IPAccessControlRuntime {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if cached, ok := s.ipAccessControlRuntimeCache.Load().(*cachedIPAccessControlRuntime); ok && cached != nil {
		if time.Now().Before(cached.expiresAt) {
			return cached.runtime
		}
		// 过期：先返回旧值，后台刷新，避免热路径阻塞在 DB 上。
		go s.refreshIPAccessControlRuntime(context.WithoutCancel(ctx))
		return cached.runtime
	}
	// 冷启动：同步加载一次。
	return s.refreshIPAccessControlRuntime(ctx)
}

func (s *SettingService) refreshIPAccessControlRuntime(ctx context.Context) *IPAccessControlRuntime {
	result, _, _ := s.ipAccessControlRuntimeSF.Do(SettingKeyIPAccessControlConfig, func() (any, error) {
		if cached, ok := s.ipAccessControlRuntimeCache.Load().(*cachedIPAccessControlRuntime); ok && cached != nil {
			if time.Now().Before(cached.expiresAt) {
				return cached.runtime, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ipAccessControlDBTimeout)
		defer cancel()
		settings, err := s.GetIPAccessControlSettings(dbCtx)
		if err != nil {
			slog.Warn("[Setting] refresh ip access control runtime failed", "error", err)
			// DB 故障：延用上一份快照（若有），短 TTL 尽快重试；否则回退为全部关闭。
			var runtime *IPAccessControlRuntime
			if cached, ok := s.ipAccessControlRuntimeCache.Load().(*cachedIPAccessControlRuntime); ok && cached != nil {
				runtime = cached.runtime
			}
			s.storeIPAccessControlRuntime(runtime, ipAccessControlErrorTTL)
			return runtime, nil
		}
		runtime := buildIPAccessControlRuntime(settings)
		s.storeIPAccessControlRuntime(runtime, ipAccessControlCacheTTL)
		return runtime, nil
	})
	runtime, _ := result.(*IPAccessControlRuntime)
	return runtime
}

// isIPv6RegistrationBlocked 报告当前请求上下文中的客户端是否因 IPv6 拦截而禁止注册。
// 依赖全局 SessionBindingContext 中间件注入的客户端 IP；缺失时不拦截。
// 由 IsRegistrationEnabled 调用，从而一次覆盖邮箱注册、验证码发送与各 OAuth 自动建号路径。
func (s *SettingService) isIPv6RegistrationBlocked(ctx context.Context) bool {
	runtime := s.GetIPAccessControlRuntimeCached(ctx)
	if runtime == nil || !runtime.Settings.IPv6BlockEnabled {
		return false
	}
	binding := SessionBindingFromContext(ctx)
	if binding == nil {
		return false
	}
	return runtime.IPv6Blocked(binding.IP)
}
