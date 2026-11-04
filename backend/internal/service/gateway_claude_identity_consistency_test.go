//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeIdentityMatchesAcrossNativeAndMimic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"messages", "count"} {
		t.Run(endpoint, func(t *testing.T) {
			resetGatewayForwardingSettingsCacheForTest(t)
			fp := &Fingerprint{UserAgent: "claude-cli/2.9.0 (external, cli)", ClientID: strings.Repeat("a", 64), StainlessOS: "Windows", StainlessArch: "x64", StainlessPackageVersion: "0.128.0", StainlessRuntime: "node", StainlessRuntimeVersion: "v26.3.0", UpdatedAt: time.Now().Unix()}
			svc := &GatewayService{identityService: NewIdentityService(&stubIdentityCache{fingerprint: fp})}
			account := &Account{ID: 941, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
			uid := FormatMetadataUserID(strings.Repeat("c", 64), "bbbbbbbb-1111-4222-8333-444444444444", "7578cf37-aaca-46e4-a45c-71285d9dbb83", "2.9.0")
			raw := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":` + strconvQuote(uid) + `}}`)
			for _, mimic := range []bool{false, true} {
				c, _ := claudeRepairContext(raw, claude.DefaultUserAgent())
				var req *http.Request
				var out []byte
				var err error
				if endpoint == "messages" {
					req, out, err = svc.buildUpstreamRequest(context.Background(), c, account, raw, "synthetic", "oauth", "claude-sonnet-4-6", false, mimic)
				} else {
					req, out, err = svc.buildCountTokensRequest(context.Background(), c, account, raw, "synthetic", "oauth", "claude-sonnet-4-6", mimic)
				}
				require.NoError(t, err)
				require.Equal(t, fp.UserAgent, getHeaderRaw(req.Header, "User-Agent"))
				require.Equal(t, fp.StainlessOS, getHeaderRaw(req.Header, "X-Stainless-OS"))
				require.Equal(t, fp.StainlessArch, getHeaderRaw(req.Header, "X-Stainless-Arch"))
				require.Equal(t, fp.StainlessPackageVersion, getHeaderRaw(req.Header, "X-Stainless-Package-Version"))
				require.Equal(t, fp.StainlessRuntimeVersion, getHeaderRaw(req.Header, "X-Stainless-Runtime-Version"))
				if endpoint == "messages" {
					meta := ParseMetadataUserID(gjson.GetBytes(out, "metadata.user_id").String())
					require.NotNil(t, meta)
					require.Equal(t, fp.ClientID, meta.DeviceID)
					require.Empty(t, meta.AccountUUID)
				}
				require.NoError(t, req.Body.Close())
			}
		})
	}
}

func TestClaudeCompatibleSessionsDistinguishCallers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"responses", "chat"} {
		t.Run(route, func(t *testing.T) {
			upstream := &anthropicHTTPUpstreamRecorder{}
			svc := claudeRepairOAuthService(t, upstream, true)
			account := newAnthropicOAuthAccountForPartialUsageTest()
			account.Extra = map[string]any{"account_uuid": "aaaaaaaa-1111-4222-8333-444444444444"}
			var ids []string
			for _, key := range []int64{101, 202, 101} {
				raw := []byte(`{"model":"claude-sonnet-4-6","input":"hello"}`)
				if route == "chat" {
					raw = []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`)
				}
				c, _ := claudeRepairContext(raw, "test-client/1.0")
				c.Set("api_key", &APIKey{ID: key})
				upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(claudeRepairSSE([]string{"ok"})))}
				var err error
				if route == "responses" {
					_, err = svc.ForwardAsResponses(context.Background(), c, account, raw, nil)
				} else {
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, raw, nil)
				}
				require.NoError(t, err)
				meta := ParseMetadataUserID(gjson.GetBytes(upstream.lastBody, "metadata.user_id").String())
				require.NotNil(t, meta)
				ids = append(ids, meta.SessionID)
			}
			require.NotEqual(t, ids[0], ids[1])
			require.Equal(t, ids[0], ids[2])
		})
	}
}

func TestClaudeSoftwareUpgradePreservesDevicePlatform(t *testing.T) {
	fp := &Fingerprint{UserAgent: "claude-cli/2.9.0 (external, cli)", StainlessOS: "Windows", StainlessArch: "x64", UpdatedAt: time.Now().Unix()}
	svc := NewIdentityService(&stubIdentityCache{fingerprint: fp})
	out, err := svc.GetOrCreateFingerprint(context.Background(), 941, http.Header{"User-Agent": {"claude-cli/2.9.1 (external, cli)"}, "X-Stainless-Os": {"Linux"}, "X-Stainless-Arch": {"arm64"}, "X-Stainless-Package-Version": {"0.129.0"}})
	require.NoError(t, err)
	require.Equal(t, "Windows", out.StainlessOS)
	require.Equal(t, "x64", out.StainlessArch)
	require.Equal(t, "0.129.0", out.StainlessPackageVersion)
}

func TestClaudeFloorDoesNotHideObservedSoftwareUpgrade(t *testing.T) {
	claude.SetCLIVersionResolver(func() string { return "2.1.300" })
	t.Cleanup(func() { claude.SetCLIVersionResolver(nil) })
	svc := NewIdentityService(&stubIdentityCache{})
	headers := http.Header{"User-Agent": {"claude-cli/2.1.200 (external, cli)"}, "X-Stainless-Package-Version": {"0.80.0"}}
	first, err := svc.GetOrCreateFingerprint(context.Background(), 942, headers)
	require.NoError(t, err)
	require.Contains(t, first.UserAgent, "2.1.300")
	headers.Set("User-Agent", "claude-cli/2.1.220 (external, cli)")
	headers.Set("X-Stainless-Package-Version", "0.90.0")
	second, err := svc.GetOrCreateFingerprint(context.Background(), 942, headers)
	require.NoError(t, err)
	require.Contains(t, second.UserAgent, "2.1.300")
	require.Equal(t, "0.90.0", second.StainlessPackageVersion)
	headers.Set("User-Agent", "claude-cli/2.1.210 (external, cli)")
	headers.Set("X-Stainless-Package-Version", "0.85.0")
	third, err := svc.GetOrCreateFingerprint(context.Background(), 942, headers)
	require.NoError(t, err)
	require.Equal(t, "0.90.0", third.StainlessPackageVersion)
}

func TestClaudeLoweredFloorDoesNotDowngradePersistedWireVersion(t *testing.T) {
	floor := "2.1.300"
	claude.SetCLIVersionResolver(func() string { return floor })
	t.Cleanup(func() { claude.SetCLIVersionResolver(nil) })
	svc := NewIdentityService(&stubIdentityCache{})
	_, err := svc.GetOrCreateFingerprint(context.Background(), 949, http.Header{"User-Agent": {"claude-cli/2.1.200 (external, cli)"}})
	require.NoError(t, err)
	floor = "2.1.280"
	out, err := svc.GetOrCreateFingerprint(context.Background(), 949, http.Header{"User-Agent": {"claude-cli/2.1.220 (external, cli)"}, "X-Stainless-Package-Version": {"0.112.1"}})
	require.NoError(t, err)
	require.Contains(t, out.UserAgent, "2.1.300")
	require.Equal(t, "0.112.1", out.StainlessPackageVersion)
}

type claudeMaskUnavailableCache struct{ stubIdentityCache }

func (*claudeMaskUnavailableCache) GetOrCreateMaskedSessionID(context.Context, int64, string) (string, error) {
	return "", errors.New("masked session cache unavailable")
}

func TestClaudeMaskingFailureBlocksOutboundRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fp := &Fingerprint{UserAgent: claude.DefaultUserAgent(), ClientID: strings.Repeat("a", 64), UpdatedAt: time.Now().Unix()}
	svc := &GatewayService{identityService: NewIdentityService(&claudeMaskUnavailableCache{stubIdentityCache{fingerprint: fp}})}
	uid := FormatMetadataUserID(strings.Repeat("c", 64), "", "7578cf37-aaca-46e4-a45c-71285d9dbb83", claude.EffectiveCLIVersion())
	raw := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":` + strconvQuote(uid) + `}}`)
	c, _ := claudeRepairContext(raw, claude.DefaultUserAgent())
	account := &Account{ID: 943, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"session_id_masking_enabled": true}}
	_, _, err := svc.buildUpstreamRequest(context.Background(), c, account, raw, "synthetic", "oauth", "claude-sonnet-4-6", false, false)
	require.ErrorContains(t, err, "masked session cache unavailable")
}
