//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The DingTalk enterprise exemption lifts the registration_enabled switch only.
// It sits in an OR around IsRegistrationEnabled at every OAuth sign-up gate, so
// without its own IPv6 check it would also lift the risk-control IPv6 block.
func TestCanBypassRegistrationDisabledForOAuth_DoesNotBypassIPv6Block(t *testing.T) {
	ipAccess, err := json.Marshal(IPAccessControlSettings{IPv6BlockEnabled: true})
	require.NoError(t, err)
	svc := newAuthServiceWithDingTalkCfg(map[string]string{
		SettingKeyRegistrationEnabled:                  "false",
		SettingKeyIPAccessControlConfig:                string(ipAccess),
		SettingKeyDingTalkConnectEnabled:               "true",
		SettingKeyDingTalkConnectBypassRegistration:    "true",
		SettingKeyDingTalkConnectCorpRestrictionPolicy: "internal_only",
	}, minDingTalkURLs())

	ipv4 := WithSessionBinding(context.Background(), &SessionBinding{IP: "8.8.8.8"})
	ipv6 := WithSessionBinding(context.Background(), &SessionBinding{IP: "2400:8902::1"})

	require.True(t, svc.canBypassRegistrationDisabledForOAuth(ipv4, "dingtalk"), "IPv4 keeps the enterprise exemption")
	require.False(t, svc.canBypassRegistrationDisabledForOAuth(ipv6, "dingtalk"), "IPv6 block must survive the exemption")
}
