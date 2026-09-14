package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The documented shape from opencode issue #44189. OpenCode sizes the 5-hour
// window at 20% of the monthly allowance and the weekly at 50%, so those two
// always bind before monthly — which is why only they are carried.
func TestParseOpenCodeUsageTiersReadsTheDocumentedShape(t *testing.T) {
	body := []byte(`{"usage":{
		"rolling":{"status":"ok","percent":12,"resetsAt":"2026-09-15T18:00:00Z"},
		"weekly":{"status":"ok","percent":34,"resetsAt":"2026-09-20T00:00:00Z"},
		"monthly":{"status":"ok","percent":56,"resetsAt":"2026-10-01T00:00:00Z"}}}`)

	tiers := parseOpenCodeUsageTiers(body)
	require.Len(t, tiers, 2, "monthly is deliberately dropped")
	require.Equal(t, CNQuotaTier{Window: "5h", UsedPercent: 12, ResetAt: "2026-09-15T18:00:00Z"}, tiers[0])
	require.Equal(t, CNQuotaTier{Window: "weekly", UsedPercent: 34, ResetAt: "2026-09-20T00:00:00Z"}, tiers[1])
}

// The schema is undocumented, so the parser has to survive plausible variation
// rather than assume one spelling.
func TestParseOpenCodeUsageTiersToleratesSchemaVariation(t *testing.T) {
	t.Run("windows at the root without a usage wrapper", func(t *testing.T) {
		tiers := parseOpenCodeUsageTiers([]byte(`{"rolling":{"percent":5},"weekly":{"percent":9}}`))
		require.Len(t, tiers, 2)
		require.Equal(t, float64(5), tiers[0].UsedPercent)
	})

	t.Run("percent expressed as remaining instead of used", func(t *testing.T) {
		tiers := parseOpenCodeUsageTiers([]byte(`{"usage":{"rolling":{"remainingPercent":80}}}`))
		require.Len(t, tiers, 1)
		require.Equal(t, float64(20), tiers[0].UsedPercent)
	})

	t.Run("snake_case keys", func(t *testing.T) {
		tiers := parseOpenCodeUsageTiers([]byte(
			`{"usage":{"weekly":{"used_percent":"41","reset_at":"2026-09-20T00:00:00Z"}}}`))
		require.Len(t, tiers, 1)
		require.Equal(t, float64(41), tiers[0].UsedPercent)
		require.Equal(t, "2026-09-20T00:00:00Z", tiers[0].ResetAt)
	})

	t.Run("a window with no reset time still reports usage", func(t *testing.T) {
		tiers := parseOpenCodeUsageTiers([]byte(`{"usage":{"rolling":{"percent":7}}}`))
		require.Len(t, tiers, 1)
		require.Empty(t, tiers[0].ResetAt)
	})
}

// A fabricated 0% is the dangerous failure: threshold-based pausing would read
// it as "plenty left" and keep scheduling an account that is actually spent.
// No recognizable window must yield no tiers, which the caller surfaces as an
// error instead of persisting.
func TestParseOpenCodeUsageTiersYieldsNothingOnUnrecognizedBodies(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"usage":{}}`,
		`{"error":{"message":"unauthorized"}}`,
		`{"usage":{"rolling":{"status":"ok"}}}`,
		`{"usage":{"rolling":"not-an-object"}}`,
		`not json at all`,
	} {
		require.Empty(t, parseOpenCodeUsageTiers([]byte(body)), "body: %s", body)
	}
}

func TestOpenCodeUsedPercentIsFlooredButNotCapped(t *testing.T) {
	// Over-consumption is real and must stay visible; the other providers'
	// parsers report it as-is too.
	over := parseOpenCodeUsageTiers([]byte(`{"usage":{"rolling":{"percent":137}}}`))
	require.Equal(t, float64(137), over[0].UsedPercent)

	// A remaining figure above 100 would otherwise produce a negative used.
	under := parseOpenCodeUsageTiers([]byte(`{"usage":{"rolling":{"remainingPercent":105}}}`))
	require.Equal(t, float64(0), under[0].UsedPercent)
}

// The quota endpoint is hardcoded so an operator-supplied relay base_url can
// never receive the API key, matching how the other providers' URLs are built.
func TestOpenCodeQuotaURLIsPinnedToTheOfficialHost(t *testing.T) {
	require.Equal(t, "https://opencode.ai/zen/go/v1/usage", openCodeQuotaURL())
}

// Only the Go subscription has a usage endpoint. Zen pay-as-you-go must resolve
// to no provider so the quota service rejects it instead of probing a URL that
// does not exist for that plan.
func TestOpenCodeCodingPlanProviderRequiresTheGoPrefix(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "go subscription", mode: AccountModeCoding, want: PlatformOpenCode},
		{name: "zen pay as you go", mode: AccountModePayG, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, openCodeAccount(tt.mode, APIProtocolAdaptive).GetCodingPlanProvider())
		})
	}

	// A relay pointed at a lookalike host must not be treated as official.
	relay := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	relay.Credentials["base_url"] = "https://opencode.ai.evil.example/zen/go/v1"
	require.Equal(t, "", relay.GetCodingPlanProvider())
}

// Quota flows through provider-keyed extra keys, so the threshold evaluator
// picks it up with no extra plumbing — but only if opencode is actually listed.
func TestOpenCodeQuotaFeedsSchedulingThresholds(t *testing.T) {
	require.Contains(t, AllowedSchedulingThresholdPlatforms, PlatformOpenCode)

	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	account.Extra = cnQuotaExtraUpdates(PlatformOpenCode, []CNQuotaTier{
		{Window: "5h", UsedPercent: 91, ResetAt: "2026-09-15T18:00:00Z"},
	}, time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))

	candidates := cnProviderThresholdCandidates(account, PlatformOpenCode)
	var found bool
	for _, c := range candidates {
		if c != nil {
			found = true
		}
	}
	require.True(t, found, "a persisted 5h window must produce a threshold candidate")
}
