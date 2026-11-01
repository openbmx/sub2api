package model

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTLSFingerprintProfileRejectsUnsupportedHTTP2ALPN(t *testing.T) {
	for _, protocol := range []string{"h2", "h2c"} {
		profile := &TLSFingerprintProfile{Name: "custom", ALPNProtocols: []string{"http/1.1", protocol}}
		require.Error(t, profile.Validate(), "HTTP/1-only transport must reject unsupported ALPN")
	}
	require.NoError(t, (&TLSFingerprintProfile{Name: "default"}).Validate())
	require.NoError(t, (&TLSFingerprintProfile{Name: "http1", ALPNProtocols: []string{"http/1.1"}}).Validate())
}
