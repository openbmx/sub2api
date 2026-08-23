//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writableSettingRepoStub 在 settingRepoStub 之上支持 Set（写入 values map）。
type writableSettingRepoStub struct {
	*settingRepoStub
}

func (s *writableSettingRepoStub) Set(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func newIPAccessSettingService(values map[string]string) (*SettingService, *writableSettingRepoStub) {
	repo := &writableSettingRepoStub{settingRepoStub: &settingRepoStub{values: values}}
	return NewSettingService(repo, nil), repo
}

func TestSetIPAccessControlSettings_ValidatesAndPersists(t *testing.T) {
	svc, repo := newIPAccessSettingService(map[string]string{})

	saved, err := svc.SetIPAccessControlSettings(context.Background(), IPAccessControlSettings{
		IPBlacklistEnabled: true,
		IPBlacklist:        []string{" 1.2.3.4 ", "10.0.0.0/8", "1.2.3.4", "", "2001:db8::/32"},
		IPBlacklistMessage: " blocked ",
	})
	require.NoError(t, err)
	// 去重 + 去空白
	require.Equal(t, []string{"1.2.3.4", "10.0.0.0/8", "2001:db8::/32"}, saved.IPBlacklist)
	require.Equal(t, "blocked", saved.IPBlacklistMessage)

	raw := repo.values[SettingKeyIPAccessControlConfig]
	require.NotEmpty(t, raw)
	var stored IPAccessControlSettings
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.True(t, stored.IPBlacklistEnabled)

	loaded, err := svc.GetIPAccessControlSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, saved, loaded)
}

func TestSetIPAccessControlSettings_RejectsInvalidEntries(t *testing.T) {
	svc, _ := newIPAccessSettingService(map[string]string{})

	_, err := svc.SetIPAccessControlSettings(context.Background(), IPAccessControlSettings{
		IPBlacklist: []string{"1.2.3.4", "not-an-ip", "999.999.0.1"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not-an-ip")

	_, err = svc.SetIPAccessControlSettings(context.Background(), IPAccessControlSettings{
		IPBlacklistMessage: strings.Repeat("长", 1001),
	})
	require.Error(t, err)
}

func TestGetIPAccessControlSettings_DefaultsWhenMissingOrCorrupt(t *testing.T) {
	svc, _ := newIPAccessSettingService(map[string]string{})
	settings, err := svc.GetIPAccessControlSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.IPBlacklistEnabled)
	require.False(t, settings.IPv6BlockEnabled)
	require.Empty(t, settings.IPBlacklist)

	svc2, _ := newIPAccessSettingService(map[string]string{
		SettingKeyIPAccessControlConfig: "{corrupt json",
	})
	settings2, err := svc2.GetIPAccessControlSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings2.IPBlacklistEnabled)
}

func TestIPAccessControlRuntime_Matching(t *testing.T) {
	svc, _ := newIPAccessSettingService(map[string]string{})
	_, err := svc.SetIPAccessControlSettings(context.Background(), IPAccessControlSettings{
		IPBlacklistEnabled: true,
		IPBlacklist:        []string{"1.2.3.4", "10.1.0.0/16", "2001:db8::/32"},
		IPv6BlockEnabled:   true,
	})
	require.NoError(t, err)

	// SetIPAccessControlSettings 保存后运行时缓存立即生效
	runtime := svc.GetIPAccessControlRuntimeCached(context.Background())
	require.True(t, runtime.Enabled())
	require.True(t, runtime.BlacklistMatches("1.2.3.4"))
	require.True(t, runtime.BlacklistMatches("10.1.200.7"))
	require.True(t, runtime.BlacklistMatches("2001:db8::9"))
	require.False(t, runtime.BlacklistMatches("8.8.8.8"))
	require.False(t, runtime.BlacklistMatches(""))
	require.False(t, runtime.BlacklistMatches("garbage"))

	// IPv6 拦截：仅公网 IPv6 命中
	require.True(t, runtime.IPv6Blocked("2400:8902::f03c:91ff:fe70:1"))
	require.False(t, runtime.IPv6Blocked("8.8.8.8"))
	require.False(t, runtime.IPv6Blocked("::ffff:8.8.8.8"))
	require.False(t, runtime.IPv6Blocked("::1"))
	require.False(t, runtime.IPv6Blocked("fe80::1"))
	require.False(t, runtime.IPv6Blocked("fc00::1"))
}

func TestIPAccessControlRuntime_DisabledDoesNotMatch(t *testing.T) {
	svc, _ := newIPAccessSettingService(map[string]string{})
	_, err := svc.SetIPAccessControlSettings(context.Background(), IPAccessControlSettings{
		IPBlacklistEnabled: false,
		IPBlacklist:        []string{"1.2.3.4"},
		IPv6BlockEnabled:   false,
	})
	require.NoError(t, err)

	runtime := svc.GetIPAccessControlRuntimeCached(context.Background())
	require.False(t, runtime.Enabled())
	require.False(t, runtime.BlacklistMatches("1.2.3.4"))
	require.False(t, runtime.IPv6Blocked("2001:db8::1"))
}

func TestIsRegistrationEnabled_BlocksPublicIPv6WhenEnabled(t *testing.T) {
	cfgJSON, err := json.Marshal(IPAccessControlSettings{IPv6BlockEnabled: true})
	require.NoError(t, err)
	svc, _ := newIPAccessSettingService(map[string]string{
		SettingKeyRegistrationEnabled:   "true",
		SettingKeyIPAccessControlConfig: string(cfgJSON),
	})

	ipv6Ctx := WithSessionBinding(context.Background(), &SessionBinding{IP: "2400:8902::1"})
	ipv4Ctx := WithSessionBinding(context.Background(), &SessionBinding{IP: "8.8.8.8"})

	require.False(t, svc.IsRegistrationEnabled(ipv6Ctx), "公网 IPv6 在 IPv6 拦截开启时应禁止注册")
	require.True(t, svc.IsRegistrationEnabled(ipv4Ctx), "IPv4 不受 IPv6 拦截影响")
	require.True(t, svc.IsRegistrationEnabled(context.Background()), "无会话绑定信息时不拦截")
}

func TestIsRegistrationEnabled_AllowsIPv6WhenBlockDisabled(t *testing.T) {
	svc, _ := newIPAccessSettingService(map[string]string{
		SettingKeyRegistrationEnabled: "true",
	})
	ipv6Ctx := WithSessionBinding(context.Background(), &SessionBinding{IP: "2400:8902::1"})
	require.True(t, svc.IsRegistrationEnabled(ipv6Ctx))
}
