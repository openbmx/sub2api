package securityaudit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The helper alone proving correct is not enough — the bug was that the audit
// module never called anything like it. These two exercise the real request
// builders at both outbound call sites.
func TestScanRequestCarriesCustomHeaders(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	_, _, err := NewOpenAICompatibleScanner().ScanVerbose(context.Background(), ActiveEndpoint{
		ID: "guard-1", Name: "Guard", Protocol: "openai_compatible", BaseURL: server.URL,
		Model: DefaultGuardModel, Token: "secret", TimeoutMS: 1000, InputLimit: 1024,
		Enabled: true, ResponseFormat: ResponseFormatQwen3Guard,
		Headers: map[string]string{"X-Tenant": "t1"},
	}, "hello", AllScannerIDs)
	require.Error(t, err)
	require.Equal(t, "t1", got.Get("X-Tenant"))
	require.Equal(t, "Bearer secret", got.Get("Authorization"))
	require.Equal(t, "application/json", got.Get("Content-Type"))
}

// Base URLs stored before the version-segment fix were normalized by the old
// rule. A probe must still match them against the freshly normalized input, or
// upgrading silently stops reusing saved tokens and headers for every node whose
// URL lives under a prefix.
func TestProbeMatchesStoredEndpointNormalizedByAnOlderRule(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":[{"id":"` + DefaultGuardModel + `"}]}`))
	}))
	defer server.Close()

	service := newProbeTestService()
	service.config = &fakeConfigStore{active: true, cfg: ActiveConfig{Endpoints: []ActiveEndpoint{{
		ID: "probe-one", BaseURL: server.URL + "/api/v1", Token: "stored-token",
		Headers: map[string]string{"X-Tenant": "t1"},
	}}}}

	endpoint := probeEndpoint(server.URL+"/api/v1", "")
	result := service.Probe(context.Background(), ProbeRequest{Endpoint: endpoint})
	require.True(t, result.OK)
	require.True(t, result.TokenApplied)
	require.Equal(t, "Bearer stored-token", got.Get("Authorization"))
	require.Equal(t, "t1", got.Get("X-Tenant"))
}

func TestProbeRequestCarriesCustomHeaders(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			got = r.Header.Clone()
			_, _ = w.Write([]byte(`{"data":[{"id":"` + DefaultGuardModel + `"}]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	endpoint := probeEndpoint(server.URL, "temporary-token")
	endpoint.Headers = map[string]string{"x-tenant": "t1"}
	result := newProbeTestService().Probe(context.Background(), ProbeRequest{Endpoint: endpoint})
	require.True(t, result.OK)
	require.Equal(t, "t1", got.Get("X-Tenant"))
	require.Equal(t, "Bearer temporary-token", got.Get("Authorization"))
}

func TestApplyEndpointHeadersLeavesNonOpenCodeUpstreamsUntouched(t *testing.T) {
	t.Parallel()
	// The regression this guards: OpenCode needs a routing header, and adding it
	// must not change a single byte for every other audit upstream people run.
	for _, baseURL := range []string{
		"http://127.0.0.1:8000", "https://api.siliconflow.cn/v1",
		"https://dashscope.aliyuncs.com/compatible-mode/v1", "https://openrouter.ai/api/v1",
		"https://api.openai.com/v1", "https://opencode.ai.evil.example/v1",
		"https://guard.internal.local:8080",
	} {
		header := http.Header{}
		applyEndpointHeaders(header, ActiveEndpoint{ID: "guard-1", BaseURL: baseURL, Token: "secret"})
		require.Equal(t, "Bearer secret", header.Get("Authorization"), baseURL)
		require.Empty(t, header.Get(openCodeSessionHeaderName), baseURL)
		require.Len(t, header, 1, baseURL)
	}
}

func TestApplyEndpointHeadersFillsOpenCodeSession(t *testing.T) {
	t.Parallel()
	for _, baseURL := range []string{
		"https://opencode.ai/zen/v1", "https://opencode.ai/go", "https://OpenCode.AI/v1",
		"https://api.opencode.ai/v1",
	} {
		header := http.Header{}
		applyEndpointHeaders(header, ActiveEndpoint{ID: "guard-1", BaseURL: baseURL, Token: "secret"})
		require.NotEmpty(t, header.Get(openCodeSessionHeaderName), baseURL)
	}
}

func TestAuditSessionIDIsStablePerNodeAndDistinctAcrossNodes(t *testing.T) {
	t.Parallel()
	// OpenCode asks for "a stable per-conversation ID". Stability across process
	// restarts is the whole point, so this must be a pure function of the node.
	first := auditSessionID("guard-1")
	require.Equal(t, first, auditSessionID("guard-1"))
	require.Equal(t, first, auditSessionID("  guard-1  "))
	require.NotEqual(t, first, auditSessionID("guard-2"))
	require.True(t, strings.HasPrefix(first, "sub2api-audit-"))
	// The operator-chosen node ID must not travel to the upstream in cleartext.
	require.NotContains(t, auditSessionID("prod-guard-openai-key-3"), "prod-guard")
}

func TestApplyEndpointHeadersOperatorValueWinsOverDerivedSession(t *testing.T) {
	t.Parallel()
	header := http.Header{}
	applyEndpointHeaders(header, ActiveEndpoint{
		ID: "guard-1", BaseURL: "https://opencode.ai/zen/v1", Token: "secret",
		Headers: map[string]string{"X-Opencode-Session": "operator-chosen"},
	})
	require.Equal(t, "operator-chosen", header.Get(openCodeSessionHeaderName))
	require.Len(t, header.Values(openCodeSessionHeaderName), 1)
}

func TestApplyEndpointHeadersCustomHeaderCanOverrideAuthorization(t *testing.T) {
	t.Parallel()
	// A gateway wanting a non-bearer scheme is the reason custom headers exist,
	// so an explicit Authorization must beat the token-derived one.
	header := http.Header{}
	applyEndpointHeaders(header, ActiveEndpoint{
		ID: "guard-1", BaseURL: "https://guard.example.com/v1", Token: "secret",
		Headers: map[string]string{"Authorization": "Basic abc", "X-Api-Key": "k"},
	})
	require.Equal(t, "Basic abc", header.Get("Authorization"))
	require.Equal(t, "k", header.Get("X-Api-Key"))
}

func TestApplyEndpointHeadersSkipsReservedAndMalformedNames(t *testing.T) {
	t.Parallel()
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	applyEndpointHeaders(header, ActiveEndpoint{
		ID: "guard-1", BaseURL: "https://guard.example.com/v1",
		Headers: map[string]string{
			"Content-Type": "text/plain", "Host": "evil.example", "Content-Length": "0",
			"bad name": "x", "X-Ok": "y",
		},
	})
	require.Equal(t, "application/json", header.Get("Content-Type"))
	require.Empty(t, header.Get("Host"))
	require.Empty(t, header.Get("Content-Length"))
	require.Empty(t, header.Get("Bad Name"))
	require.Equal(t, "y", header.Get("X-Ok"))
}

func TestNormalizeCustomHeadersAcceptsAndCanonicalizes(t *testing.T) {
	t.Parallel()
	result, err := normalizeCustomHeaders(map[string]string{
		"  x-opencode-session  ": "  abc  ", "X-TENANT": "t1", "": "dropped blank row",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"X-Opencode-Session": "abc", "X-Tenant": "t1"}, result)
}

func TestNormalizeCustomHeadersDistinguishesOmittedFromCleared(t *testing.T) {
	t.Parallel()
	// nil means "field not sent, keep what is stored"; an empty map is an
	// explicit removal. Collapsing the two would let an older admin client wipe
	// headers on an unrelated save.
	omitted, err := normalizeCustomHeaders(nil)
	require.NoError(t, err)
	require.Nil(t, omitted)

	cleared, err := normalizeCustomHeaders(map[string]string{})
	require.NoError(t, err)
	require.NotNil(t, cleared)
	require.Empty(t, cleared)
}

func TestNormalizeCustomHeadersRejectsBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{"invalid name", map[string]string{"X Tenant": "v"}},
		{"colon in name", map[string]string{"X-Tenant:": "v"}},
		{"reserved", map[string]string{"content-type": "text/plain"}},
		{"host", map[string]string{"Host": "evil.example"}},
		{"header injection", map[string]string{"X-Tenant": "a\r\nX-Admin: 1"}},
		{"newline", map[string]string{"X-Tenant": "a\nb"}},
		{"value too long", map[string]string{"X-Tenant": strings.Repeat("v", MaxCustomHeaderValueRunes+1)}},
		{"duplicate after canonicalization", map[string]string{"x-tenant": "a", "X-Tenant": "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeCustomHeaders(tt.headers)
			require.Error(t, err)
		})
	}
}

func TestNormalizeCustomHeadersRejectsTooMany(t *testing.T) {
	t.Parallel()
	headers := make(map[string]string, MaxCustomHeaders+1)
	for i := 0; i <= MaxCustomHeaders; i++ {
		headers["X-Tenant-"+string(rune('a'+i))] = "v"
	}
	_, err := normalizeCustomHeaders(headers)
	require.Error(t, err)
}

func TestSanitizeCustomHeadersDropsRatherThanFails(t *testing.T) {
	t.Parallel()
	// The load path must survive a config the save path would have rejected,
	// losing the offending header instead of the whole audit configuration.
	require.Nil(t, sanitizeCustomHeaders(nil))
	require.Equal(t, map[string]string{"X-Ok": "v"}, sanitizeCustomHeaders(map[string]string{
		"X-Ok": "v", "bad name": "x", "Host": "evil.example", "X-Inject": "a\r\nb",
	}))
}

func TestActiveFromStorageCarriesHeadersToTheScanner(t *testing.T) {
	t.Parallel()
	cfg := DefaultStorageConfig()
	cfg.Endpoints = []StorageEndpoint{{
		ID: "guard-1", Name: "Guard", Protocol: "openai_compatible",
		BaseURL: "https://opencode.ai/zen/v1", Model: "m", TimeoutMS: DefaultTimeoutMS,
		InputLimit: DefaultInputLimit, Enabled: true, ResponseFormat: ResponseFormatQwen3Guard,
		Headers: map[string]string{"X-Tenant": "t1"},
	}}
	active, err := ActiveFromStorage(cfg, true, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"X-Tenant": "t1"}, active.Endpoints[0].Headers)

	// The runtime snapshot must not alias the stored map.
	active.Endpoints[0].Headers["X-Tenant"] = "mutated"
	require.Equal(t, "t1", cfg.Endpoints[0].Headers["X-Tenant"])
}

func TestPublicFromStorageExposesHeadersForRoundTripEditing(t *testing.T) {
	t.Parallel()
	cfg := DefaultStorageConfig()
	cfg.Endpoints = []StorageEndpoint{{
		ID: "guard-1", Name: "Guard", Protocol: "openai_compatible", BaseURL: "https://guard.example.com/v1",
		Model: "m", TimeoutMS: DefaultTimeoutMS, InputLimit: DefaultInputLimit, Enabled: true,
		ResponseFormat: ResponseFormatQwen3Guard, Headers: map[string]string{"X-Tenant": "t1"},
	}}
	public := PublicFromStorage(cfg, true, nil)
	require.Equal(t, map[string]string{"X-Tenant": "t1"}, public.Endpoints[0].Headers)
}

func TestParseStorageConfigAcceptsConfigsWithoutHeaders(t *testing.T) {
	t.Parallel()
	// Every config saved before this field existed decodes with no headers key.
	raw := `{"enabled":false,"strategy":"priority","worker_count":4,"queue_capacity":32768,
	"all_groups":true,"endpoints":[{"id":"guard-1","name":"Guard","protocol":"openai_compatible",
	"base_url":"http://127.0.0.1:8000","model":"m","timeout_ms":3000,"input_limit":4000,"enabled":true}]}`
	cfg, err := ParseStorageConfig(raw)
	require.NoError(t, err)
	require.Nil(t, cfg.Endpoints[0].Headers)

	active, err := ActiveFromStorage(cfg, true, nil)
	require.NoError(t, err)
	header := http.Header{}
	applyEndpointHeaders(header, active.Endpoints[0])
	require.Empty(t, header)
}
