package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Platform allowlists are duplicated across gin `binding:"oneof=..."` tags, ent
// enum Values(...) and SQL CHECK constraints — all as bare string literals.
// Adding a platform by grepping for the Go constant (PlatformMiniMax and
// friends) structurally cannot find any of them, which is exactly how the
// v1.1.18 OpenCode rollout shipped with six of them stale: groups and channel
// monitors could not be created for the new platform at all.
//
// This test reads the literals back out and checks them against the Go
// constants, so the next platform fails here instead of in production.

var oneofPlatformTagPattern = regexp.MustCompile(`oneof=([a-z_ ]+)"`)

// concretePlatforms is every platform an account can actually be created on.
func concretePlatforms() []string {
	return []string{
		service.PlatformAnthropic, service.PlatformOpenAI, service.PlatformGemini,
		service.PlatformAntigravity, service.PlatformGrok, service.PlatformKimi,
		service.PlatformZhipu, service.PlatformDeepseek, service.PlatformMiniMax,
		service.PlatformOpenCode,
	}
}

func TestGroupPlatformBindingTagsCoverEveryConcretePlatform(t *testing.T) {
	source := readRepoFile(t, "internal", "handler", "admin", "group_handler.go")

	// Group platform tags additionally accept composite; route targets do not.
	assertOneofCoversPlatforms(t, source, "platform\"", concretePlatforms())
	assertOneofCoversPlatforms(t, source, "target_platform\"", concretePlatforms())
}

func TestChannelMonitorProviderBindingTagsCoverEveryConcretePlatform(t *testing.T) {
	source := readRepoFile(t, "internal", "handler", "admin", "channel_monitor_handler.go")
	// Monitors have no composite provider, so the concrete list is the whole set.
	assertOneofCoversPlatforms(t, source, "provider\"", concretePlatforms())
}

// The ent enum is a second, independent copy of the monitor provider list. A
// value accepted by the handler but missing here fails at insert time.
func TestChannelMonitorEntEnumsCoverEveryConcretePlatform(t *testing.T) {
	for _, schema := range []string{"channel_monitor.go", "channel_monitor_request_template.go"} {
		source := readRepoFile(t, "ent", "schema", schema)
		for _, platform := range concretePlatforms() {
			require.Contains(t, source, `"`+platform+`"`,
				"%s ent enum is missing %q", schema, platform)
		}
	}
}

// assertOneofCoversPlatforms finds every oneof tag on a line mentioning
// fieldJSONName and requires each platform to appear in it.
func assertOneofCoversPlatforms(t *testing.T, source, fieldJSONName string, platforms []string) {
	t.Helper()
	var checked int
	for _, line := range strings.Split(source, "\n") {
		if !strings.Contains(line, fieldJSONName) {
			continue
		}
		match := oneofPlatformTagPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		allowed := strings.Fields(match[1])
		for _, platform := range platforms {
			require.Contains(t, allowed, platform,
				"oneof tag for %s is missing %q: %s", fieldJSONName, platform, strings.TrimSpace(line))
		}
		checked++
	}
	require.NotZero(t, checked, "found no oneof tag for %s — did the field move or get renamed?", fieldJSONName)
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	// Tests run from the package directory; walk back to the backend root.
	path := filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
	content, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	return string(content)
}
