package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenCodePlatformMigration(t *testing.T) {
	content, err := FS.ReadFile("238_add_opencode_platform.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode'))")
	require.Contains(t, sql,
		"CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode'))")
	require.Contains(t, sql,
		"CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok', 'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode'))")
}

// The ent schema carries a build-time copy of the quota platform list,
// maintained by hand alongside this migration and service.AllowedQuotaPlatforms
// (see the note in user_platform_quota.go). Adding a platform to the database
// constraint but not to the schema validator passes every other test and then
// rejects the first row written.
func TestQuotaPlatformListsAgreeBetweenMigrationAndEntSchema(t *testing.T) {
	schema, err := os.ReadFile(filepath.Join("..", "ent", "schema", "user_platform_quota.go"))
	require.NoError(t, err)

	migration, err := FS.ReadFile("238_add_opencode_platform.sql")
	require.NoError(t, err)
	normalized := strings.Join(strings.Fields(string(migration)), " ")

	for _, platform := range []string{
		"anthropic", "openai", "gemini", "antigravity", "grok",
		"kimi", "zhipu", "deepseek", "minimax", "opencode",
	} {
		require.Contains(t, normalized, "'"+platform+"'", "migration is missing %q", platform)
		require.Contains(t, string(schema), `"`+platform+`"`, "ent schema validator is missing %q", platform)
	}
}
