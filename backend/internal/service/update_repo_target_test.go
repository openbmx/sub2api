//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The updater downloads and installs a binary over the running one, so it must
// resolve against this fork's own releases. Pointing it upstream would install
// artifacts built from a different tree.
func TestUpdateGitHubRepoDefaultsToThisFork(t *testing.T) {
	t.Setenv(updateGitHubRepoEnv, "")
	require.Equal(t, "openbmx/sub2api", updateGitHubRepo())
	require.Equal(t, "openbmx/sub2api", defaultGitHubRepo,
		"the default must name this fork, not the upstream repository")
}

func TestUpdateGitHubRepoHonoursEnvOverride(t *testing.T) {
	t.Setenv(updateGitHubRepoEnv, "someone-else/sub2api")
	require.Equal(t, "someone-else/sub2api", updateGitHubRepo())
}

// A blank or whitespace-only override must not produce an empty repository
// path, which would make every release request malformed.
func TestUpdateGitHubRepoIgnoresBlankOverride(t *testing.T) {
	for _, blank := range []string{"", "   ", "\t"} {
		t.Setenv(updateGitHubRepoEnv, blank)
		require.Equal(t, defaultGitHubRepo, updateGitHubRepo())
	}
}
