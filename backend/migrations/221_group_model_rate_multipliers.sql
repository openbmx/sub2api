-- Per-model extra billing multiplier, stacked on top of the effective group multiplier.
-- Shape: {"claude-opus-4.5":1.5,"gpt-5.5*":0.8}
-- Key matching: exact name wins; otherwise longest matching trailing-* prefix wins.
-- Stacking order in billing: (system → group → user override) × peak × THIS.
-- Applies to the text / image / video billing branches; web-search and audio are
-- per-call/per-duration capabilities without a model dimension and stay untouched.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS model_rate_multipliers JSONB;

COMMENT ON COLUMN groups.model_rate_multipliers IS
    '可选：按模型叠乘的额外计费倍率。key 为精确模型名或末尾 * 前缀通配（如 claude-opus-4*），value 为倍率；精确匹配优先，其次最长前缀。NULL/空表示不叠乘（等价 1.0）。允许 0（该模型免费）；负数/NaN/Inf 视为脏数据按 1.0 降级';
