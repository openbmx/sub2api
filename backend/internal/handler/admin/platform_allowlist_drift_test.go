package admin

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Platform allowlists used to be duplicated across gin `binding:"oneof=..."`
// tags, ent enum Values(...) and SQL CHECK constraints — all bare string
// literals that grepping for a Go constant cannot find. That is how the v1.1.18
// OpenCode rollout shipped with six of them stale: groups and channel monitors
// could not be created for the new platform at all.
//
// Upstream v0.2.17 removed the hazard for groups and composite routes: they are
// validated against the platform catalog (platform_validation.go, migration 242
// dropped their CHECKs). What is left to guard is a merge bringing a hand-written
// list back, and the channel monitor provider list, which upstream deliberately
// keeps out of the catalog and still copies into handler, ent and SQL.

// Driving the whole catalog through the real binders fails a merge that brings
// a hand-written whitelist back as soon as the next platform is registered; the
// unknown platform fails one that drops the validation altogether.
func TestGroupPlatformBindingFollowsThePlatformCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bind := func(target any, body string) error {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c.ShouldBindJSON(target)
	}
	for _, p := range append(domain.ConcretePlatformIDs(), domain.PlatformComposite) {
		require.NoError(t, bind(&CreateGroupRequest{}, fmt.Sprintf(`{"name":"g","platform":%q}`, p)), p)
		require.NoError(t, bind(&UpdateGroupRequest{}, fmt.Sprintf(`{"platform":%q}`, p)), p)
	}
	for _, p := range domain.ConcretePlatformIDs() {
		require.NoError(t, bind(&CompositeRouteRequest{}, fmt.Sprintf(`{"public_model":"m","target_platform":%q}`, p)), p)
	}

	const unknown = "no-such-platform"
	require.Error(t, bind(&CreateGroupRequest{}, fmt.Sprintf(`{"name":"g","platform":%q}`, unknown)))
	require.Error(t, bind(&UpdateGroupRequest{}, fmt.Sprintf(`{"platform":%q}`, unknown)))
	require.Error(t, bind(&CompositeRouteRequest{}, fmt.Sprintf(`{"public_model":"m","target_platform":%q}`, unknown)))
}

// Monitor providers are a capability list (only platforms with a probe adapter),
// so command_code / cline / typesafe are absent on purpose. The hazard left is
// the copies drifting apart: a value the handler accepts but the database CHECK
// rejects fails only at insert time.
//
// validateProvider's maps (channel_monitor_validate.go) are deliberately not
// pinned here: upstream left opencode_go out of them, so the console offers an
// OpenCode Go monitor that the service then rejects. Closing that also needs the
// probe to send x-opencode-session, which is separate work.
var (
	providerOneofPattern   = regexp.MustCompile(`json:"provider" binding:"[a-z,]*oneof=([a-z_ ]+)"`)
	entProviderEnumPattern = regexp.MustCompile(`field\.Enum\("provider"\)\.\s*Values\(([^)]*)\)`)
	sqlCommentPattern      = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
	monitorCheckDDLPattern = regexp.MustCompile(`(?i)(DROP|ADD)\s+CONSTRAINT\s+(?:IF\s+EXISTS\s+)?(channel_monitors_provider_check|channel_monitor_request_templates_provider_check)\b(?:\s+CHECK\s*\(\s*provider\s+IN\s*\(([^)]*)\))?`)
	quotedValuePattern     = regexp.MustCompile(`["']([a-z_]+)["']`)
)

func TestChannelMonitorProviderListsAgree(t *testing.T) {
	tags := providerOneofPattern.FindAllStringSubmatch(
		readRepoFile(t, "internal", "handler", "admin", "channel_monitor_handler.go"), -1)
	require.Len(t, tags, 2, "expected the create and update provider oneof tags")
	want := sortedCopy(strings.Fields(tags[0][1]))
	require.Equal(t, want, sortedCopy(strings.Fields(tags[1][1])), "create/update provider oneof tags differ")

	for _, schema := range []string{"channel_monitor.go", "channel_monitor_request_template.go"} {
		m := entProviderEnumPattern.FindStringSubmatch(readRepoFile(t, "ent", "schema", schema))
		require.NotNil(t, m, "%s: provider enum not found", schema)
		require.Equal(t, want, quotedValues(m[1]), "%s provider enum differs from the handler oneof", schema)
	}

	// Replay the provider CHECK DDL in execution (filename) order; a later DROP
	// without a re-ADD means the database no longer constrains the value.
	names, err := fs.Glob(dbmigrations.FS, "*.sql")
	require.NoError(t, err)
	sort.Strings(names)
	checks := map[string][]string{}
	for _, name := range names {
		content, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		sql := sqlCommentPattern.ReplaceAllString(string(content), "")
		for _, m := range monitorCheckDDLPattern.FindAllStringSubmatch(sql, -1) {
			constraint := strings.ToLower(m[2])
			if strings.EqualFold(m[1], "DROP") {
				delete(checks, constraint)
				continue
			}
			require.NotEmpty(t, m[3], "%s: unrecognised %s definition", name, constraint)
			checks[constraint] = quotedValues(m[3])
		}
	}
	require.Len(t, checks, 2, "both provider CHECK constraints should be in force after all migrations")
	for constraint, values := range checks {
		require.Equal(t, want, values, "%s differs from the handler oneof", constraint)
	}
}

func quotedValues(s string) []string {
	var out []string
	for _, m := range quotedValuePattern.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return sortedCopy(out)
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	// Tests run from the package directory; walk back to the backend root.
	path := filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
	content, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	return string(content)
}
