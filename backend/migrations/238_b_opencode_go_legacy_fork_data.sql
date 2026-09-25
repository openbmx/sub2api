-- 补齐 238_a 没有覆盖到的 fork 时期 OpenCode 数据。
--
-- 238_a 只把各表的 platform 列从 'opencode' 改成了上游的 'opencode_go'。本仓库在
-- v1.1.17–v1.1.21 自建 OpenCode 平台时，还有三类数据用的是 fork 自己的写法，改名后
-- 上游代码读不懂，会被静默当成别的东西：
--
-- 1. credentials.account_mode：fork 沿用国产供应商那套 'payg'（Zen 按量）/
--    'coding'（Go 订阅）；上游只认 'zen' / 'go'，且非 'zen' 一律按 Go 处理
--    （GetOpenCodeAccountMode）。不改的话存量 Zen 账号会用 Go 的协议表选端点，
--    余额不足的 402 还会走 handleAuthError 被永久置为 error（IsCNProvider 已不含
--    opencode_go）。fork 对缺失值按 Zen 处理、上游按 Go，所以缺失值只在 base_url
--    能明确判断时才补，避免误改上游之后新建的账号。
--
-- 2. credentials.opencode_model_protocols：fork 的「模型前缀 → 协议」覆盖表，上游
--    不再读取，改读 protocol_rules（[{pattern, protocol}]，pattern 为精确 ID 或末尾 *）。
--    上游一旦配置了 protocol_rules 就只查这张表，未命中直接走 Chat Completions、不再
--    回落内置默认表；而 fork 的语义是「覆盖优先，其余走内置表」。所以转换时把该模式
--    的上游默认规则（DefaultOpenCodeGoProtocolRules / DefaultOpenCodeZenProtocolRules）
--    追加在覆盖项之后。旧键原样保留，已没有代码读取它。
--
-- 3. 以平台名为键或值的配置：渠道定价、渠道模型映射、账号统计定价、错误透传规则、
--    告警静默、monitor v2 配置，以及 account_scheduling_thresholds /
--    default_platform_quotas 两个设置项。不改的话 OpenCode 渠道定价匹配不上
--    opencode_go 分组（计费落回全局价目表），阈值与额度设置被当作未知平台丢弃。
--
-- 历史指标表（ops_* 聚合、monitor v2 分钟/小时桶）故意不动：只影响看板上旧数据的
-- 归属，而且它们按 (bucket, platform, ...) 唯一，升级后重算已经写入了 'opencode_go'
-- 行，改名会撞唯一约束，让整个升级失败。
--
-- 文件名排在 238_a 之后；已经跑过 238/239 的库在下次启动时也会执行本文件（执行器按
-- 文件名判断是否已执行，不要求顺序）。全新安装和没用过 fork OpenCode 的库上全部是
-- no-op；每条语句都可安全重复执行。

-- 1. account_mode ---------------------------------------------------------------

UPDATE accounts
SET credentials = jsonb_set(credentials, '{account_mode}', '"zen"'::jsonb)
WHERE platform = 'opencode_go'
  AND credentials ->> 'account_mode' = 'payg';

UPDATE accounts
SET credentials = jsonb_set(credentials, '{account_mode}', '"go"'::jsonb)
WHERE platform = 'opencode_go'
  AND credentials ->> 'account_mode' = 'coding';

UPDATE accounts
SET credentials = jsonb_set(credentials, '{account_mode}', '"go"'::jsonb)
WHERE platform = 'opencode_go'
  AND COALESCE(credentials ->> 'account_mode', '') = ''
  AND credentials ->> 'base_url' ILIKE '%opencode.ai/zen/go%';

UPDATE accounts
SET credentials = jsonb_set(credentials, '{account_mode}', '"zen"'::jsonb)
WHERE platform = 'opencode_go'
  AND COALESCE(credentials ->> 'account_mode', '') = ''
  AND credentials ->> 'base_url' ILIKE '%opencode.ai/zen%'
  AND credentials ->> 'base_url' NOT ILIKE '%opencode.ai/zen/go%';

-- 2. opencode_model_protocols -> protocol_rules ---------------------------------
-- 依赖上一步：默认规则按已规范化的 account_mode 选择。覆盖项按前缀长度降序排列，
-- 让更具体的前缀先命中（fork 用 map 遍历，重叠前缀时的顺序本就不确定）。
-- 不合法的项（空白、含 *、超长、未知协议）直接跳过，与上游 protocol_rules 校验一致。

UPDATE accounts AS a
SET credentials = a.credentials || jsonb_build_object('protocol_rules', conv.rules)
FROM (
    SELECT acc.id,
           overrides.rules || CASE
               WHEN acc.credentials ->> 'account_mode' = 'zen' THEN
                   '[{"pattern":"grok-*","protocol":"responses"},
                     {"pattern":"gpt-*","protocol":"responses"},
                     {"pattern":"muse-spark-*","protocol":"responses"},
                     {"pattern":"claude-*","protocol":"anthropic"},
                     {"pattern":"qwen*","protocol":"anthropic"}]'::jsonb
               ELSE
                   '[{"pattern":"grok-*","protocol":"responses"},
                     {"pattern":"gpt-*","protocol":"responses"},
                     {"pattern":"muse-spark-*","protocol":"responses"},
                     {"pattern":"minimax-*","protocol":"anthropic"},
                     {"pattern":"qwen*","protocol":"anthropic"}]'::jsonb
           END AS rules
    FROM accounts AS acc
    CROSS JOIN LATERAL (
        SELECT jsonb_agg(
                   jsonb_build_object('pattern', lower(btrim(o.key)) || '*', 'protocol', o.value)
                   ORDER BY length(btrim(o.key)) DESC, lower(btrim(o.key))
               ) AS rules
        FROM jsonb_each_text(acc.credentials -> 'opencode_model_protocols') AS o(key, value)
        WHERE btrim(o.key) <> ''
          AND btrim(o.key) !~ '[[:space:]*]'
          AND length(btrim(o.key)) < 128
          AND o.value IN ('chat_completions', 'anthropic', 'responses')
    ) AS overrides
    WHERE acc.platform = 'opencode_go'
      AND jsonb_typeof(acc.credentials -> 'opencode_model_protocols') = 'object'
      AND acc.credentials -> 'protocol_rules' IS NULL
      AND overrides.rules IS NOT NULL
) AS conv
WHERE a.id = conv.id;

-- 3. 以平台名为键或值的配置 ------------------------------------------------------

UPDATE channel_model_pricing SET platform = 'opencode_go' WHERE platform = 'opencode';
UPDATE channel_account_stats_model_pricing SET platform = 'opencode_go' WHERE platform = 'opencode';

-- ops_alert_silences 在绝大多数库里并不存在：037 把 goose 的 Down 段（DROP TABLE）也写在
-- 同一个文件里，而迁移执行器整文件执行，建表后随即删表，之后没有迁移再建它。
-- 直接 UPDATE 会以 "relation does not exist" 让本迁移乃至整次升级失败。
DO $$
BEGIN
    IF to_regclass('ops_alert_silences') IS NOT NULL THEN
        UPDATE ops_alert_silences SET platform = 'opencode_go' WHERE platform = 'opencode';
    END IF;
END $$;

-- 嵌套映射 {"<platform>": {"src": "dst"}}：若两个键同时存在，合并时 opencode_go 的条目优先。
UPDATE channels
SET model_mapping = CASE
        WHEN jsonb_typeof(model_mapping -> 'opencode_go') = 'object' THEN
            (model_mapping - 'opencode')
            || jsonb_build_object('opencode_go', (model_mapping -> 'opencode') || (model_mapping -> 'opencode_go'))
        ELSE
            (model_mapping - 'opencode')
            || jsonb_build_object('opencode_go', model_mapping -> 'opencode')
    END
WHERE jsonb_typeof(model_mapping) = 'object'
  AND jsonb_typeof(model_mapping -> 'opencode') = 'object';

UPDATE error_passthrough_rules AS r
SET platforms = (
        SELECT COALESCE(jsonb_agg(DISTINCT CASE WHEN t.p = 'opencode' THEN 'opencode_go' ELSE t.p END), '[]'::jsonb)
        FROM jsonb_array_elements_text(r.platforms) AS t(p)
    )
WHERE jsonb_typeof(r.platforms) = 'array'
  AND r.platforms @> '["opencode"]'::jsonb;

-- monitor v2 配置：[{platform, enabled, models}]。已有 opencode_go 项时丢弃旧项；
-- version 是乐观锁计数，改了配置就递增，让未刷新的管理端表单保存失败而不是覆盖。
UPDATE channel_monitor_v2_config AS c
SET platforms = (
        SELECT COALESCE(jsonb_agg(
                   CASE WHEN t.elem ->> 'platform' = 'opencode'
                        THEN jsonb_set(t.elem, '{platform}', '"opencode_go"'::jsonb)
                        ELSE t.elem
                   END
                   ORDER BY t.ord), '[]'::jsonb)
        FROM jsonb_array_elements(c.platforms) WITH ORDINALITY AS t(elem, ord)
        WHERE NOT (t.elem ->> 'platform' = 'opencode'
                   AND c.platforms @> '[{"platform":"opencode_go"}]'::jsonb)
    ),
    version = c.version + 1,
    updated_at = NOW()
WHERE jsonb_typeof(c.platforms) = 'array'
  AND c.platforms @> '[{"platform":"opencode"}]'::jsonb;

-- settings.value 是 TEXT。逐个键处理：某个值不是合法 JSON 时只跳过它并告警，
-- 既不挡住整个升级，也不连带另一个键。已有 opencode_go 键时保留它、丢弃旧键。
DO $$
DECLARE
    setting_key TEXT;
BEGIN
    FOREACH setting_key IN ARRAY ARRAY['account_scheduling_thresholds', 'default_platform_quotas'] LOOP
        BEGIN
            UPDATE settings
            SET value = (CASE
                    WHEN value::jsonb -> 'opencode_go' IS NOT NULL THEN value::jsonb - 'opencode'
                    ELSE (value::jsonb - 'opencode') || jsonb_build_object('opencode_go', value::jsonb -> 'opencode')
                END)::text,
                updated_at = NOW()
            WHERE key = setting_key
              AND value LIKE '%"opencode"%'
              AND jsonb_typeof(value::jsonb) = 'object'
              AND value::jsonb -> 'opencode' IS NOT NULL;
        EXCEPTION WHEN others THEN
            RAISE WARNING '238_b: skipped setting % (%): %', setting_key, SQLSTATE, SQLERRM;
        END;
    END LOOP;
END $$;
