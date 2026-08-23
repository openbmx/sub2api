//go:build unit

package ip

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsPublicIPv6(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"2400:8902::f03c:91ff:fe70:1", true},
		{"2001:db8::1", true},
		{"[2400:8902::1]:443", true}, // 带端口自动剥离
		{"8.8.8.8", false},           // IPv4
		{"::ffff:8.8.8.8", false},    // IPv4-mapped
		{"::1", false},               // 环回
		{"fe80::1", false},           // 链路本地
		{"fc00::1", false},           // ULA
		{"fd12:3456::1", false},      // ULA
		{"::", false},                // unspecified
		{"", false},
		{"not-an-ip", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, IsPublicIPv6(tt.ip), tt.ip)
	}
}

func TestMatchesCompiledRules(t *testing.T) {
	rules := CompileIPRules([]string{"1.2.3.4", "10.0.0.0/8", "2001:db8::/32"})
	require.True(t, MatchesCompiledRules("1.2.3.4", rules))
	require.True(t, MatchesCompiledRules("10.20.30.40", rules))
	require.True(t, MatchesCompiledRules("2001:db8:1::5", rules))
	require.True(t, MatchesCompiledRules("1.2.3.4:8080", rules)) // 带端口自动剥离
	require.False(t, MatchesCompiledRules("5.6.7.8", rules))
	require.False(t, MatchesCompiledRules("", rules))
	require.False(t, MatchesCompiledRules("1.2.3.4", nil))
	require.False(t, MatchesCompiledRules("1.2.3.4", CompileIPRules(nil)))
}
