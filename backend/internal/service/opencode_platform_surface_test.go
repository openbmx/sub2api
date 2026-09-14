package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Adding a platform means touching a dozen independent allowlists. The v1.1.18
// rollout missed six of them because the sweep grepped for the Go constant and
// a paginated result was mistaken for a complete one — so the misses only
// surfaced as runtime 400s. These assertions pin the ones with user-visible
// consequences, each naming what breaks when it regresses.
func TestOpenCodeIsRegisteredAcrossThePlatformSurface(t *testing.T) {
	t.Run("scheduling keeps the platform distinct", func(t *testing.T) {
		// Normalizing to openai would make an OpenCode group schedule onto
		// OpenAI accounts and vice versa — silent cross-platform bleed.
		require.Equal(t, PlatformOpenCode, NormalizeOpenAICompatiblePlatform(PlatformOpenCode))
	})

	t.Run("scheduler snapshots include the platform", func(t *testing.T) {
		// Missing here means no scheduler buckets are ever built, so accounts
		// exist but never get picked.
		require.Contains(t, schedulerSnapshotPlatforms(), PlatformOpenCode)
	})

	t.Run("account creation accepts the upstream billing probe", func(t *testing.T) {
		// This is what actually blocked account creation: the admin form enables
		// the probe by default, and an unsupported identity is a hard 400
		// ("account is not an API key account") before anything is persisted.
		require.True(t, IsUpstreamBillingProbeIdentity(PlatformOpenCode, AccountTypeAPIKey))
		require.False(t, IsUpstreamBillingProbeIdentity(PlatformOpenCode, AccountTypeOAuth),
			"only API-key accounts carry a static key to present")
	})

	t.Run("official endpoints short-circuit the billing probe", func(t *testing.T) {
		// opencode.ai cannot host /v1/sub2api/billing, so probing it would send
		// the account key to a path that cannot exist.
		require.True(t, upstreamBillingProbeTargetIsOfficialAPI("https://opencode.ai/zen/go/v1"))
		require.True(t, upstreamBillingProbeTargetIsOfficialAPI("https://opencode.ai/zen/v1"))
		// A third-party relay still gets probed.
		require.False(t, upstreamBillingProbeTargetIsOfficialAPI("https://relay.example.com/v1"))
	})

	t.Run("request routing and group matching include the platform", func(t *testing.T) {
		require.True(t, isConcreteRequestPlatform(PlatformOpenCode))
		require.Contains(t, matchingPlatforms(PlatformComposite), PlatformOpenCode)
		require.Contains(t, AllowedQuotaPlatforms, PlatformOpenCode)
	})

	t.Run("deliberate exclusions stay excluded", func(t *testing.T) {
		// Ollama Cloud keys are hosted under openai/anthropic/CN platforms;
		// OpenCode is not a host for them.
		require.False(t, isOllamaCloudUsagePlatform(PlatformOpenCode))
	})
}
