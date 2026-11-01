package dto

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountRandomTLSProfileSurvivesDTO(t *testing.T) {
	account := &service.Account{ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": int64(-1)}}
	got := AccountFromServiceShallow(account)
	require.NotNil(t, got.TLSFingerprintProfileID)
	require.Equal(t, int64(-1), *got.TLSFingerprintProfileID)
}
