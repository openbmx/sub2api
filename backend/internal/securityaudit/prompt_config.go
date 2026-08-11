package securityaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	DefaultWorkerCount   = 4
	MaxWorkerCount       = 32
	DefaultQueueCapacity = 32768
	MaxQueueCapacity     = 100000
	DefaultTimeoutMS     = 3000
	MinTimeoutMS         = 100
	// MaxTimeoutMS bounds the budget for a whole evaluation, not one upstream
	// call: PromptGuard.Evaluate derives a single context from it and every
	// chunk shares it. A one-shot scan of a large transcript on a reasoning
	// model needs far more than the previous 30s ceiling.
	MaxTimeoutMS      = 120000
	DefaultInputLimit = 4000
	MinInputLimit     = 128
	// MaxInputLimit is the per-chunk character ceiling. The 4000 default suits
	// small classifiers such as qwen3guard; large-context audit models can take
	// a whole transcript in one call, which is both faster than N sequential
	// chunks and safer — chunking splits the text, so an attack straddling a
	// boundary is evaluated as two innocuous halves. Raised so an operator can
	// set input_limit above the largest transcript they expect and get exactly
	// one upstream call. Defaults are unchanged.
	MaxInputLimit     = 1000000
	DefaultPayloadTTL = 30 * time.Minute

	// DefaultBlockHTTPStatus keeps the historical 403. The status is
	// administrator-configurable because different clients react differently,
	// but it is constrained to 4xx: a block is a client-side policy outcome,
	// and a 5xx would tell well-behaved clients to retry the same rejected
	// prompt. 429 is accepted but is a poor choice for the same reason — it
	// means "retry later" and invites exactly the retry loop an operator is
	// usually trying to stop.
	DefaultBlockHTTPStatus = 403
	MinBlockHTTPStatus     = 400
	MaxBlockHTTPStatus     = 499
	MaxBlockMessageRunes   = 500

	// DefaultTurnScanRunes caps the latest turn handed to a synchronous audit.
	//
	// An agent turn is not a typed prompt. Observed on a live deployment: a
	// 65 000-character dotnet build log arriving as the turn to audit, costing
	// roughly 25 000 uncached tokens per call and breaking the audit three ways
	// — the reasoning budget ran out before a verdict, the reply came back
	// unusable, or the evaluation timed out.
	//
	// Oversized turns are sampled head and tail rather than cut short, because
	// that is where injected instructions sit. It is still a trade: tool output
	// is a real indirect-injection vector and an injection buried mid-log is
	// missed. Raise this when false negatives matter more than cost; lower it
	// when the reverse holds. Zero disables sampling entirely.
	DefaultTurnScanRunes = 1000
	MinTurnScanRunes     = 200
	MaxTurnScanRunes     = 100000

	// DefaultNodeConcurrency caps in-flight audits per node. It used to be a
	// hardcoded 16, which made a saturated node indistinguishable from a broken
	// one: the 17th request fails in single-digit milliseconds with no endpoint
	// and no upstream call, exactly like a hard failure. It matters more as
	// timeout_ms grows, because each slot is held for the whole evaluation —
	// at a 120s budget a burst of parallel agent requests can hold every slot
	// for two minutes.
	DefaultNodeConcurrency = 16
	MinNodeConcurrency     = 1
	MaxNodeConcurrency     = 256

	// DefaultBlockVerdictTTL is how long a block is replayed for an identical
	// prompt. Audit models are not deterministic — the same prompt measured
	// eight times against deepseek-v4-flash at temperature 0 scored between
	// 0.1 and 0.9 — so without this a client simply retries until a run falls
	// below the threshold. Only blocks are cached: caching a pass would freeze
	// one lucky false negative in place for the whole window.
	DefaultBlockVerdictTTL = 10 * time.Minute
	// DefaultPassVerdictTTL caches a pass for far less time than a block. An
	// agent re-sends near-identical turns seconds apart, and every one of those
	// is a paid audit call, so a short window removes most of them. Keeping it
	// short is the safety margin: a verdict that was wrong to allow expires in
	// under a minute instead of being replayed for the whole block window.
	DefaultPassVerdictTTL = 45 * time.Second
	// maxBlockVerdictEntries bounds the in-process cache. Entries are small and
	// expire quickly; the cap only exists so a flood of distinct blocked
	// prompts cannot grow it without limit.
	maxBlockVerdictEntries = 4096
)

// DefaultBlockMessage is the client-facing text used when an administrator has
// not set one.
const DefaultBlockMessage = "提示词安全审计拒绝了该请求，请调整输入后重试"

type SecretEncryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// ConfigStore is the injectable boundary between hot-path prompt auditing and
// the concrete settings/PostgreSQL/Redis-backed configuration manager.
type ConfigStore interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Active() (ActiveConfig, bool)
	EffectiveMode() Mode
	// BlockingActivationDegraded is true when storage intent requires blocking
	// but no usable blocking snapshot is active (cold start or failed reload).
	// It must stay false when blocking is not intended, even if config is
	// untrusted—otherwise default-off deployments fail closed for all traffic.
	BlockingActivationDegraded() bool
	// BlockingFailOpen reports whether a failed blocking audit should pass the
	// request through. It must answer from the last decodable storage intent
	// even when the full config could not be activated, otherwise a degraded
	// reload would silently ignore the administrator's choice.
	BlockingFailOpen() bool
	Public() (PublicConfig, error)
	Save(ctx context.Context, req UpdateConfigRequest, actorID int64) (PublicConfig, error)
	RuntimeState() (expected int64, active int64, loadedAt *time.Time, loadError string)
	Encrypt(value string) (string, error)
	Decrypt(value string) (string, error)
}

type StorageEndpoint struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Protocol        string `json:"protocol"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	TokenCiphertext string `json:"token_ciphertext,omitempty"`
	TimeoutMS       int    `json:"timeout_ms"`
	InputLimit      int    `json:"input_limit"`
	Enabled         bool   `json:"enabled"`
	// ResponseFormat selects the request/response contract for this node.
	// Empty in configs saved before custom prompts existed; normalization
	// backfills ResponseFormatQwen3Guard so their behavior is unchanged.
	ResponseFormat string `json:"response_format"`
}

type storageConfig struct {
	Enabled                bool `json:"enabled"`
	BlockingEnabled        bool `json:"blocking_enabled"`
	BlockingLatestTurnOnly bool `json:"blocking_latest_turn_only"`
	// BlockingFailOpen passes a request through when the audit could not be
	// completed. Default false keeps the established fail-closed posture; issue
	// #3678 asks for fail-open so audit availability never gates traffic.
	BlockingFailOpen bool              `json:"blocking_fail_open"`
	StorePassEvents  bool              `json:"store_pass_events"`
	Strategy         string            `json:"strategy"`
	WorkerCount      int               `json:"worker_count"`
	QueueCapacity    int               `json:"queue_capacity"`
	Scanners         []string          `json:"scanners"`
	AllGroups        bool              `json:"all_groups"`
	GroupIDs         []int64           `json:"group_ids"`
	Endpoints        []StorageEndpoint `json:"endpoints"`
	// CustomPrompt is the administrator-authored moderation policy shared by
	// every custom_json endpoint, so it is edited once rather than per node.
	CustomPrompt   string  `json:"custom_prompt"`
	BlockThreshold float64 `json:"block_threshold"`
	FlagThreshold  float64 `json:"flag_threshold"`
	// BlockHTTPStatus and BlockMessage shape the response a blocked client
	// sees. Absent in configs saved before they existed, in which case the
	// defaults apply.
	BlockHTTPStatus int    `json:"block_http_status"`
	BlockMessage    string `json:"block_message"`
	// NodeConcurrency caps simultaneous audits per node. Zero means default.
	NodeConcurrency int `json:"node_concurrency"`
	// TurnScanRunes caps the latest turn sent to a blocking audit. Zero means
	// default; -1 is stored when an operator explicitly disables sampling.
	TurnScanRunes int       `json:"turn_scan_runes"`
	ConfigVersion int64     `json:"config_version"`
	UpdatedAt     time.Time `json:"updated_at"`
	UpdatedBy     int64     `json:"updated_by"`
	ChangeSummary string    `json:"change_summary"`
}

type ActiveEndpoint struct {
	ID         string
	Name       string
	Protocol   string
	BaseURL    string
	Model      string
	Token      string
	TimeoutMS  int
	InputLimit int
	Enabled    bool
	// ResponseFormat, CustomPrompt and the thresholds are denormalized onto the
	// endpoint by ActiveFromStorage. ActiveEndpoint is runtime-only and never
	// persisted, so carrying the global policy here lets PromptScanner.Scan keep
	// its narrow signature instead of threading ActiveConfig through failover.
	ResponseFormat string
	CustomPrompt   string
	BlockThreshold float64
	FlagThreshold  float64
	// TokenInvalid marks an endpoint whose persisted token ciphertext cannot be
	// decrypted with the current encryption key (key changed or auto-generated
	// on restart). The endpoint is kept visible for admins but excluded from
	// runtime use until the token is re-entered or cleared (issue #4887).
	TokenInvalid bool
}

type ActiveConfig struct {
	RiskControlEnabled     bool
	Enabled                bool
	BlockingEnabled        bool
	BlockingLatestTurnOnly bool
	BlockingFailOpen       bool
	StorePassEvents        bool
	Strategy               string
	WorkerCount            int
	QueueCapacity          int
	Scanners               []string
	AllGroups              bool
	GroupIDs               []int64
	Endpoints              []ActiveEndpoint
	CustomPrompt           string
	BlockThreshold         float64
	FlagThreshold          float64
	BlockHTTPStatus        int
	BlockMessage           string
	NodeConcurrency        int
	TurnScanRunes          int
	ConfigVersion          int64
	UpdatedAt              time.Time
	UpdatedBy              int64
	ChangeSummary          string
}

type PublicEndpoint struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
	TimeoutMS      int    `json:"timeout_ms"`
	InputLimit     int    `json:"input_limit"`
	Enabled        bool   `json:"enabled"`
	HasToken       bool   `json:"has_token"`
	TokenStatus    string `json:"token_status"`
	ResponseFormat string `json:"response_format"`
}

type PublicConfig struct {
	Enabled                bool             `json:"enabled"`
	BlockingEnabled        bool             `json:"blocking_enabled"`
	BlockingLatestTurnOnly bool             `json:"blocking_latest_turn_only"`
	BlockingFailOpen       bool             `json:"blocking_fail_open"`
	StorePassEvents        bool             `json:"store_pass_events"`
	EffectiveMode          Mode             `json:"effective_mode"`
	Strategy               string           `json:"strategy"`
	WorkerCount            int              `json:"worker_count"`
	QueueCapacity          int              `json:"queue_capacity"`
	Scanners               []string         `json:"scanners"`
	AllGroups              bool             `json:"all_groups"`
	GroupIDs               []int64          `json:"group_ids"`
	Endpoints              []PublicEndpoint `json:"endpoints"`
	CustomPrompt           string           `json:"custom_prompt"`
	DefaultCustomPrompt    string           `json:"default_custom_prompt"`
	// CategoryAwareCustomPrompt is offered as an alternative preset because the
	// default contract reports no categories, which leaves the scanner toggles
	// with nothing to act on.
	CategoryAwareCustomPrompt string  `json:"category_aware_custom_prompt"`
	BlockThreshold            float64 `json:"block_threshold"`
	FlagThreshold             float64 `json:"flag_threshold"`
	// Effective values, already defaulted, so the UI never renders a raw zero
	// for a config saved before these fields existed.
	BlockHTTPStatus int       `json:"block_http_status"`
	BlockMessage    string    `json:"block_message"`
	NodeConcurrency int       `json:"node_concurrency"`
	TurnScanRunes   int       `json:"turn_scan_runes"`
	ConfigVersion   int64     `json:"config_version"`
	UpdatedAt       time.Time `json:"updated_at"`
	UpdatedBy       int64     `json:"updated_by"`
	ChangeSummary   string    `json:"change_summary"`
}

type UpdateEndpoint struct {
	ID             string `json:"id" binding:"required"`
	Name           string `json:"name" binding:"required"`
	Protocol       string `json:"protocol"`
	BaseURL        string `json:"base_url" binding:"required"`
	Model          string `json:"model"`
	Token          string `json:"token,omitempty"`
	ClearToken     bool   `json:"clear_token"`
	TimeoutMS      int    `json:"timeout_ms"`
	InputLimit     int    `json:"input_limit"`
	Enabled        bool   `json:"enabled"`
	ResponseFormat string `json:"response_format"`
}

type UpdateConfigRequest struct {
	ExpectedConfigVersion  int64            `json:"expected_config_version" binding:"required"`
	Enabled                bool             `json:"enabled"`
	BlockingEnabled        bool             `json:"blocking_enabled"`
	BlockingLatestTurnOnly bool             `json:"blocking_latest_turn_only"`
	BlockingFailOpen       bool             `json:"blocking_fail_open"`
	StorePassEvents        bool             `json:"store_pass_events"`
	Strategy               string           `json:"strategy"`
	WorkerCount            int              `json:"worker_count"`
	QueueCapacity          int              `json:"queue_capacity"`
	Scanners               []string         `json:"scanners"`
	AllGroups              bool             `json:"all_groups"`
	GroupIDs               []int64          `json:"group_ids"`
	Endpoints              []UpdateEndpoint `json:"endpoints"`
	CustomPrompt           string           `json:"custom_prompt"`
	BlockThreshold         float64          `json:"block_threshold"`
	FlagThreshold          float64          `json:"flag_threshold"`
	// Zero and empty mean "leave as stored", matching how CustomPrompt and the
	// thresholds behave, so an older admin client cannot reset them by omission.
	BlockHTTPStatus int    `json:"block_http_status"`
	BlockMessage    string `json:"block_message"`
	NodeConcurrency int    `json:"node_concurrency"`
	// TurnScanRunes: zero keeps the stored value, -1 disables sampling.
	TurnScanRunes int `json:"turn_scan_runes"`
}

func DefaultStorageConfig() storageConfig {
	return storageConfig{
		Enabled:                false,
		BlockingEnabled:        false,
		BlockingLatestTurnOnly: false,
		BlockingFailOpen:       false,
		StorePassEvents:        false,
		Strategy:               "priority",
		WorkerCount:            DefaultWorkerCount,
		QueueCapacity:          DefaultQueueCapacity,
		Scanners:               append([]string(nil), AllScannerIDs...),
		AllGroups:              true,
		GroupIDs:               []int64{},
		Endpoints:              []StorageEndpoint{},
		CustomPrompt:           DefaultCustomAuditPrompt,
		BlockThreshold:         DefaultBlockThreshold,
		FlagThreshold:          DefaultFlagThreshold,
		ConfigVersion:          1,
	}
}

func ParseStorageConfig(raw string) (storageConfig, error) {
	cfg := DefaultStorageConfig()
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return storageConfig{}, fmt.Errorf("decode prompt audit config: %w", err)
	}
	normalizeStorageConfig(&cfg)
	if err := validateStorageConfig(cfg); err != nil {
		return storageConfig{}, err
	}
	return cfg, nil
}

func normalizeStorageConfig(cfg *storageConfig) {
	if cfg == nil {
		return
	}
	if cfg.ConfigVersion < 1 {
		cfg.ConfigVersion = 1
	}
	if strings.TrimSpace(cfg.Strategy) == "" {
		cfg.Strategy = "priority"
	}
	if cfg.WorkerCount == 0 {
		cfg.WorkerCount = DefaultWorkerCount
	}
	if cfg.QueueCapacity == 0 {
		cfg.QueueCapacity = DefaultQueueCapacity
	}
	if len(cfg.Scanners) == 0 {
		cfg.Scanners = append([]string(nil), AllScannerIDs...)
	}
	cfg.Scanners = canonicalScannerIDs(cfg.Scanners)
	cfg.GroupIDs = canonicalInt64s(cfg.GroupIDs)
	cfg.CustomPrompt = strings.TrimSpace(cfg.CustomPrompt)
	// Configs written before custom prompts existed decode these as zero. Filling
	// defaults here—rather than rejecting—keeps existing deployments working with
	// exactly their previous behavior.
	if cfg.CustomPrompt == "" {
		cfg.CustomPrompt = DefaultCustomAuditPrompt
	}
	if cfg.BlockThreshold <= 0 || cfg.BlockThreshold > 1 {
		cfg.BlockThreshold = DefaultBlockThreshold
	}
	if cfg.FlagThreshold <= 0 || cfg.FlagThreshold > 1 {
		cfg.FlagThreshold = DefaultFlagThreshold
	}
	// Preserve an invalid blocking-without-audit combination so validation can
	// reject it instead of silently changing administrator intent.
	for i := range cfg.Endpoints {
		ep := &cfg.Endpoints[i]
		ep.ID = strings.TrimSpace(ep.ID)
		ep.Name = strings.TrimSpace(ep.Name)
		ep.Protocol = strings.TrimSpace(ep.Protocol)
		if ep.Protocol == "" {
			ep.Protocol = "openai_compatible"
		}
		ep.BaseURL = strings.TrimSpace(ep.BaseURL)
		ep.Model = strings.TrimSpace(ep.Model)
		if ep.Model == "" {
			ep.Model = DefaultGuardModel
		}
		if ep.TimeoutMS == 0 {
			ep.TimeoutMS = DefaultTimeoutMS
		}
		if ep.InputLimit == 0 {
			ep.InputLimit = DefaultInputLimit
		}
		ep.ResponseFormat = strings.TrimSpace(ep.ResponseFormat)
		if ep.ResponseFormat == "" {
			ep.ResponseFormat = ResponseFormatQwen3Guard
		}
	}
}

func isKnownResponseFormat(value string) bool {
	switch strings.TrimSpace(value) {
	case ResponseFormatQwen3Guard, ResponseFormatCustomJSON:
		return true
	default:
		return false
	}
}

// validateModerationPolicy checks the fields shared by storage and update
// validation so a request and a persisted config can never disagree.
func validateModerationPolicy(customPrompt string, blockThreshold, flagThreshold float64) error {
	if len([]rune(strings.TrimSpace(customPrompt))) > MaxCustomPromptRunes {
		return infraerrors.BadRequest(ErrorCodeCustomPromptTooLong, "自定义审核提示词超出长度上限")
	}
	if blockThreshold != 0 && (blockThreshold <= 0 || blockThreshold > 1) {
		return infraerrors.BadRequest(ErrorCodeInvalidThreshold, "阻断阈值必须位于 0 与 1 之间")
	}
	if flagThreshold != 0 && (flagThreshold <= 0 || flagThreshold > 1) {
		return infraerrors.BadRequest(ErrorCodeInvalidThreshold, "标记阈值必须位于 0 与 1 之间")
	}
	if blockThreshold != 0 && flagThreshold != 0 && flagThreshold > blockThreshold {
		return infraerrors.BadRequest(ErrorCodeInvalidThreshold, "标记阈值不能高于阻断阈值")
	}
	return nil
}

func validateStorageConfig(cfg storageConfig) error {
	if cfg.BlockingEnabled && !cfg.Enabled {
		return infraerrors.BadRequest(ErrorCodeRequiresEnabled, "开启同步阻止前必须先启用提示词审计")
	}
	if cfg.Strategy != "priority" {
		return infraerrors.BadRequest("prompt_audit_invalid_strategy", "提示词审计策略仅支持 priority")
	}
	if cfg.WorkerCount < 1 || cfg.WorkerCount > MaxWorkerCount {
		return infraerrors.BadRequest("prompt_audit_invalid_worker_count", "Worker 数量超出允许范围")
	}
	if cfg.QueueCapacity < 1 || cfg.QueueCapacity > MaxQueueCapacity {
		return infraerrors.BadRequest("prompt_audit_invalid_queue_capacity", "队列容量超出允许范围")
	}
	if !cfg.AllGroups && len(cfg.GroupIDs) == 0 {
		return infraerrors.BadRequest("prompt_audit_groups_required", "指定分组模式至少需要选择一个分组")
	}
	if len(cfg.Scanners) == 0 {
		return infraerrors.BadRequest("prompt_audit_scanners_required", "至少需要启用一个风险分类")
	}
	if err := validateModerationPolicy(cfg.CustomPrompt, cfg.BlockThreshold, cfg.FlagThreshold); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(cfg.Endpoints))
	enabled := 0
	customJSONEnabled := false
	for _, ep := range cfg.Endpoints {
		if ep.ID == "" || ep.Name == "" {
			return infraerrors.BadRequest("prompt_audit_invalid_endpoint", "审计节点 ID 和名称不能为空")
		}
		if _, ok := seen[ep.ID]; ok {
			return infraerrors.BadRequest("prompt_audit_duplicate_endpoint", "审计节点 ID 不能重复")
		}
		seen[ep.ID] = struct{}{}
		if ep.Protocol != "openai_compatible" {
			return infraerrors.BadRequest("prompt_audit_invalid_endpoint_protocol", "审计节点仅支持 OpenAI 兼容协议")
		}
		if !isKnownResponseFormat(ep.ResponseFormat) {
			return infraerrors.BadRequest(ErrorCodeInvalidResponseFormat, "审计节点响应契约无效")
		}
		if _, err := NormalizeBaseURL(ep.BaseURL); err != nil {
			return err
		}
		if ep.TimeoutMS < MinTimeoutMS || ep.TimeoutMS > MaxTimeoutMS {
			return infraerrors.BadRequest("prompt_audit_invalid_timeout", "审计节点超时超出允许范围")
		}
		if ep.InputLimit < MinInputLimit || ep.InputLimit > MaxInputLimit {
			return infraerrors.BadRequest("prompt_audit_invalid_input_limit", "审计节点输入上限超出允许范围")
		}
		if ep.Enabled {
			enabled++
			if ep.ResponseFormat == ResponseFormatCustomJSON {
				customJSONEnabled = true
			}
		}
	}
	if cfg.Enabled && enabled == 0 {
		return infraerrors.BadRequest("prompt_audit_endpoint_required", "启用提示词审计前至少需要启用一个审计节点")
	}
	if customJSONEnabled && strings.TrimSpace(cfg.CustomPrompt) == "" {
		return infraerrors.BadRequest(ErrorCodeCustomPromptRequired, "启用自定义 JSON 契约的节点前必须填写审核提示词")
	}
	return nil
}

func validateUpdateConfigRequest(req UpdateConfigRequest) error {
	if strings.TrimSpace(req.Strategy) != "priority" {
		return infraerrors.BadRequest("prompt_audit_invalid_strategy", "提示词审计策略仅支持 priority")
	}
	if req.WorkerCount < 1 || req.WorkerCount > MaxWorkerCount {
		return infraerrors.BadRequest("prompt_audit_invalid_worker_count", "Worker 数量超出允许范围")
	}
	if req.QueueCapacity < 1 || req.QueueCapacity > MaxQueueCapacity {
		return infraerrors.BadRequest("prompt_audit_invalid_queue_capacity", "队列容量超出允许范围")
	}
	if len(req.Scanners) == 0 {
		return infraerrors.BadRequest("prompt_audit_scanners_required", "至少需要启用一个风险分类")
	}
	for _, scanner := range req.Scanners {
		if _, ok := ScannerCatalog[NormalizeCategory(scanner)]; !ok {
			return infraerrors.BadRequest("prompt_audit_invalid_scanner", "提示词审计风险分类无效")
		}
	}
	if !req.AllGroups {
		if len(req.GroupIDs) == 0 {
			return infraerrors.BadRequest("prompt_audit_groups_required", "指定分组模式至少需要选择一个分组")
		}
		for _, groupID := range req.GroupIDs {
			if groupID <= 0 {
				return infraerrors.BadRequest("prompt_audit_invalid_group", "提示词审计分组 ID 无效")
			}
		}
	}
	if err := validateModerationPolicy(req.CustomPrompt, req.BlockThreshold, req.FlagThreshold); err != nil {
		return err
	}
	if err := validateBlockResponse(req.BlockHTTPStatus, req.BlockMessage); err != nil {
		return err
	}
	// Zero means "keep stored", matching the other optional policy fields.
	if req.NodeConcurrency != 0 && (req.NodeConcurrency < MinNodeConcurrency || req.NodeConcurrency > MaxNodeConcurrency) {
		return infraerrors.BadRequest("prompt_audit_invalid_node_concurrency", "单节点并发上限超出允许范围")
	}
	// -1 is the explicit "scan the whole turn" choice, so only values between
	// zero and the minimum are actually nonsense.
	if req.TurnScanRunes != 0 && req.TurnScanRunes != -1 &&
		(req.TurnScanRunes < MinTurnScanRunes || req.TurnScanRunes > MaxTurnScanRunes) {
		return infraerrors.BadRequest("prompt_audit_invalid_turn_scan_runes", "单轮送审字符上限超出允许范围")
	}
	for _, endpoint := range req.Endpoints {
		if endpoint.TimeoutMS < MinTimeoutMS || endpoint.TimeoutMS > MaxTimeoutMS {
			return infraerrors.BadRequest("prompt_audit_invalid_timeout", "审计节点超时超出允许范围")
		}
		if endpoint.InputLimit < MinInputLimit || endpoint.InputLimit > MaxInputLimit {
			return infraerrors.BadRequest("prompt_audit_invalid_input_limit", "审计节点输入上限超出允许范围")
		}
		// An empty response_format is accepted and backfilled by normalization so
		// older admin clients can keep saving configs unchanged.
		if strings.TrimSpace(endpoint.ResponseFormat) != "" && !isKnownResponseFormat(endpoint.ResponseFormat) {
			return infraerrors.BadRequest(ErrorCodeInvalidResponseFormat, "审计节点响应契约无效")
		}
	}
	return nil
}

func (cfg ActiveConfig) EffectiveMode() Mode {
	if !cfg.RiskControlEnabled || !cfg.Enabled {
		return ModeOff
	}
	if cfg.BlockingEnabled {
		return ModeBlocking
	}
	return ModeAsync
}

func (cfg ActiveConfig) IncludesGroup(groupID *int64) bool {
	if cfg.AllGroups {
		return true
	}
	if groupID == nil {
		return false
	}
	i := sort.Search(len(cfg.GroupIDs), func(i int) bool { return cfg.GroupIDs[i] >= *groupID })
	return i < len(cfg.GroupIDs) && cfg.GroupIDs[i] == *groupID
}

func (cfg ActiveConfig) EnabledEndpoints() []ActiveEndpoint {
	result := make([]ActiveEndpoint, 0, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		if ep.Enabled {
			result = append(result, ep)
		}
	}
	return result
}

// validateBlockResponse bounds the administrator-configurable block response.
// Zero and empty are accepted and mean "keep whatever is stored", which is how
// an admin client that predates these fields behaves.
func validateBlockResponse(status int, message string) error {
	if status != 0 && (status < MinBlockHTTPStatus || status > MaxBlockHTTPStatus) {
		// 5xx is rejected on purpose: it tells a client the server failed and
		// the same prompt is worth retrying, which is the opposite of a policy
		// block.
		return infraerrors.BadRequest("prompt_audit_invalid_block_status", "拦截响应状态码必须在 400-499 之间")
	}
	if utf8.RuneCountInString(strings.TrimSpace(message)) > MaxBlockMessageRunes {
		return infraerrors.BadRequest("prompt_audit_block_message_too_long", "拦截提示文案过长")
	}
	return nil
}

// EffectiveTurnScanRunes returns the latest-turn cap. A stored -1 is an
// operator explicitly turning sampling off, and is passed through as 0 so the
// snapshot builder leaves the turn whole; anything else out of range falls back
// to the default, which is also what a config saved before this field gets.
func (cfg ActiveConfig) EffectiveTurnScanRunes() int {
	if cfg.TurnScanRunes < 0 {
		return 0
	}
	if cfg.TurnScanRunes < MinTurnScanRunes || cfg.TurnScanRunes > MaxTurnScanRunes {
		return DefaultTurnScanRunes
	}
	return cfg.TurnScanRunes
}

// EffectiveNodeConcurrency returns the per-node in-flight cap, falling back to
// the default for configs saved before the field existed.
func (cfg ActiveConfig) EffectiveNodeConcurrency() int {
	if cfg.NodeConcurrency < MinNodeConcurrency || cfg.NodeConcurrency > MaxNodeConcurrency {
		return DefaultNodeConcurrency
	}
	return cfg.NodeConcurrency
}

// BlockResponse returns the effective status and message for a blocked
// request, falling back to the defaults for configs saved before these fields
// existed so behaviour is unchanged until an administrator opts in.
func (cfg ActiveConfig) BlockResponse() (status int, message string) {
	status, message = cfg.BlockHTTPStatus, strings.TrimSpace(cfg.BlockMessage)
	if status < MinBlockHTTPStatus || status > MaxBlockHTTPStatus {
		status = DefaultBlockHTTPStatus
	}
	if message == "" {
		message = DefaultBlockMessage
	}
	return status, message
}

// InvalidTokenEndpointIDs lists endpoints whose stored token could not be
// decrypted with the current encryption key.
func (cfg ActiveConfig) InvalidTokenEndpointIDs() []string {
	ids := make([]string, 0)
	for _, ep := range cfg.Endpoints {
		if ep.TokenInvalid {
			ids = append(ids, ep.ID)
		}
	}
	return ids
}

func PublicFromStorage(cfg storageConfig, riskControlEnabled bool, invalidTokenEndpointIDs []string) PublicConfig {
	invalid := make(map[string]struct{}, len(invalidTokenEndpointIDs))
	for _, id := range invalidTokenEndpointIDs {
		invalid[id] = struct{}{}
	}
	scanners := append([]string{}, cfg.Scanners...)
	groupIDs := append([]int64{}, cfg.GroupIDs...)
	endpoints := make([]PublicEndpoint, 0, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		hasToken := strings.TrimSpace(ep.TokenCiphertext) != ""
		status := "missing"
		if hasToken {
			status = "configured"
			if _, ok := invalid[ep.ID]; ok {
				status = "invalid"
			}
		}
		endpoints = append(endpoints, PublicEndpoint{
			ID: ep.ID, Name: ep.Name, Protocol: ep.Protocol, BaseURL: ep.BaseURL,
			Model: ep.Model, TimeoutMS: ep.TimeoutMS, InputLimit: ep.InputLimit,
			Enabled: ep.Enabled, HasToken: hasToken, TokenStatus: status,
			ResponseFormat: ep.ResponseFormat,
		})
	}
	active := ActiveConfig{RiskControlEnabled: riskControlEnabled, Enabled: cfg.Enabled, BlockingEnabled: cfg.BlockingEnabled}
	// Surface the effective values, not the raw zeros, so the admin UI shows
	// what a blocked client would actually receive on a config that predates
	// these fields.
	blockStatus, blockMessage := ActiveConfig{
		BlockHTTPStatus: cfg.BlockHTTPStatus, BlockMessage: cfg.BlockMessage,
	}.BlockResponse()
	return PublicConfig{
		Enabled: cfg.Enabled, BlockingEnabled: cfg.BlockingEnabled, BlockingLatestTurnOnly: cfg.BlockingLatestTurnOnly,
		BlockingFailOpen: cfg.BlockingFailOpen, StorePassEvents: cfg.StorePassEvents,
		EffectiveMode: active.EffectiveMode(), Strategy: cfg.Strategy, WorkerCount: cfg.WorkerCount,
		QueueCapacity: cfg.QueueCapacity, Scanners: scanners, AllGroups: cfg.AllGroups,
		GroupIDs: groupIDs, Endpoints: endpoints,
		// DefaultCustomPrompt lets the admin UI offer "restore default" without
		// duplicating the template in frontend code.
		CustomPrompt: cfg.CustomPrompt, DefaultCustomPrompt: DefaultCustomAuditPrompt,
		CategoryAwareCustomPrompt: CategoryAwareAuditPrompt,
		BlockThreshold:            cfg.BlockThreshold, FlagThreshold: cfg.FlagThreshold,
		BlockHTTPStatus: blockStatus, BlockMessage: blockMessage,
		NodeConcurrency: ActiveConfig{NodeConcurrency: cfg.NodeConcurrency}.EffectiveNodeConcurrency(),
		TurnScanRunes:   ActiveConfig{TurnScanRunes: cfg.TurnScanRunes}.EffectiveTurnScanRunes(),
		ConfigVersion:   cfg.ConfigVersion,
		UpdatedAt:       cfg.UpdatedAt, UpdatedBy: cfg.UpdatedBy, ChangeSummary: cfg.ChangeSummary,
	}
}

func ActiveFromStorage(cfg storageConfig, riskControlEnabled bool, encryptor SecretEncryptor) (ActiveConfig, error) {
	active := ActiveConfig{
		RiskControlEnabled: riskControlEnabled, Enabled: cfg.Enabled, BlockingEnabled: cfg.BlockingEnabled,
		BlockingLatestTurnOnly: cfg.BlockingLatestTurnOnly, BlockingFailOpen: cfg.BlockingFailOpen,
		StorePassEvents: cfg.StorePassEvents, Strategy: cfg.Strategy, WorkerCount: cfg.WorkerCount,
		QueueCapacity: cfg.QueueCapacity, Scanners: append([]string(nil), cfg.Scanners...), AllGroups: cfg.AllGroups,
		GroupIDs: append([]int64(nil), cfg.GroupIDs...), ConfigVersion: cfg.ConfigVersion,
		UpdatedAt: cfg.UpdatedAt, UpdatedBy: cfg.UpdatedBy, ChangeSummary: cfg.ChangeSummary,
		CustomPrompt: cfg.CustomPrompt, BlockThreshold: cfg.BlockThreshold, FlagThreshold: cfg.FlagThreshold,
		BlockHTTPStatus: cfg.BlockHTTPStatus, BlockMessage: cfg.BlockMessage,
		NodeConcurrency: cfg.NodeConcurrency, TurnScanRunes: cfg.TurnScanRunes,
		Endpoints: make([]ActiveEndpoint, 0, len(cfg.Endpoints)),
	}
	for _, ep := range cfg.Endpoints {
		token := ""
		tokenInvalid := false
		if ep.TokenCiphertext != "" {
			if encryptor == nil {
				return ActiveConfig{}, fmt.Errorf("prompt audit secret encryptor unavailable")
			}
			plain, err := encryptor.Decrypt(ep.TokenCiphertext)
			if err != nil {
				// An undecryptable token (encryption key changed or regenerated)
				// must not take the whole config down: admins would otherwise be
				// locked out of the real config version and unable to recover
				// (issue #4887). Keep the ciphertext persisted, but exclude the
				// endpoint from runtime use until the token is re-entered.
				tokenInvalid = true
			} else {
				token = plain
			}
		}
		responseFormat := strings.TrimSpace(ep.ResponseFormat)
		if responseFormat == "" {
			responseFormat = ResponseFormatQwen3Guard
		}
		active.Endpoints = append(active.Endpoints, ActiveEndpoint{
			ID: ep.ID, Name: ep.Name, Protocol: ep.Protocol, BaseURL: ep.BaseURL, Model: ep.Model,
			Token: token, TimeoutMS: ep.TimeoutMS, InputLimit: ep.InputLimit,
			Enabled: ep.Enabled && !tokenInvalid, TokenInvalid: tokenInvalid,
			// The global policy is copied onto every node so the scanner can stay
			// a pure (endpoint, chunk) function across failover and probe paths.
			ResponseFormat: responseFormat, CustomPrompt: cfg.CustomPrompt,
			BlockThreshold: cfg.BlockThreshold, FlagThreshold: cfg.FlagThreshold,
		})
	}
	return active, nil
}

func changeSummary(cfg storageConfig) string {
	customJSONCount := 0
	for _, ep := range cfg.Endpoints {
		if ep.ResponseFormat == ResponseFormatCustomJSON {
			customJSONCount++
		}
	}
	summary := struct {
		Enabled                bool    `json:"enabled"`
		BlockingEnabled        bool    `json:"blocking_enabled"`
		BlockingLatestTurnOnly bool    `json:"blocking_latest_turn_only"`
		BlockingFailOpen       bool    `json:"blocking_fail_open"`
		StorePassEvents        bool    `json:"store_pass_events"`
		EndpointCount          int     `json:"endpoint_count"`
		CustomJSONCount        int     `json:"custom_json_endpoint_count"`
		ScannerCount           int     `json:"scanner_count"`
		AllGroups              bool    `json:"all_groups"`
		GroupCount             int     `json:"group_count"`
		GroupHash              string  `json:"group_hash"`
		BlockThreshold         float64 `json:"block_threshold"`
		FlagThreshold          float64 `json:"flag_threshold"`
		// Only the prompt's length and digest are summarized: the text itself is
		// administrator content and must not be duplicated into settings history.
		CustomPromptRunes string `json:"custom_prompt_runes"`
		CustomPromptHash  string `json:"custom_prompt_hash"`
	}{cfg.Enabled, cfg.BlockingEnabled, cfg.BlockingLatestTurnOnly, cfg.BlockingFailOpen, cfg.StorePassEvents,
		len(cfg.Endpoints), customJSONCount, len(cfg.Scanners), cfg.AllGroups, len(cfg.GroupIDs), "",
		cfg.BlockThreshold, cfg.FlagThreshold, customPromptDigestSummary(cfg.CustomPrompt), ""}
	rawGroups, _ := json.Marshal(cfg.GroupIDs)
	digest := sha256.Sum256(rawGroups)
	summary.GroupHash = hex.EncodeToString(digest[:])
	promptDigest := sha256.Sum256([]byte(cfg.CustomPrompt))
	summary.CustomPromptHash = hex.EncodeToString(promptDigest[:])
	raw, _ := json.Marshal(summary)
	return string(raw)
}

func canonicalInt64s(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func canonicalScannerIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		id := NormalizeCategory(value)
		if _, ok := ScannerCatalog[id]; ok {
			seen[id] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for _, id := range AllScannerIDs {
		if _, ok := seen[id]; ok {
			result = append(result, id)
		}
	}
	return result
}
