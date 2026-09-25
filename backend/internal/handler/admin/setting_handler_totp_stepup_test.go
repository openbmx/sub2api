package admin

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Turning site-wide TOTP off drops 2FA from password login. With step-up on,
// that is the first move of "reset the admin password, log in again, re-enrol
// TOTP, pass step-up", so it is gated like turning step-up itself off.
func TestUpdateSettingsDisableTotpRequiresStepUp(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyStepUpEnabled: "true",
		service.SettingKeyTotpEnabled:   "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"step_up_enabled": true, "totp_enabled": false}, nil)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyTotpEnabled])
}

// With the step-up feature off nothing changes: the save goes through as before.
func TestUpdateSettingsDisableTotpWithoutStepUpFeatureSaves(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTotpEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"totp_enabled": false}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[service.SettingKeyTotpEnabled])
}
