-- 把 fork 自建的 'opencode' 平台标识改名为上游的 'opencode_go'。
--
-- 背景：本仓库在 v1.1.17–v1.1.21 独立实现过 OpenCode 平台，标识为 'opencode'
-- （见已废弃的 238_add_opencode_platform.sql）。上游随后自建了同一平台，标识
-- 为 'opencode_go'。本次合并决定改用上游实现，存量数据必须跟着改名。
--
-- 为什么必须在 238_opencode_go_platform.sql 之前执行：
--   1. 文件名排序保证本文件先跑（'238_a' < '238_o'）。
--   2. 上游那个迁移会把四个 CHECK 约束重建为**只含 'opencode_go'**。若存量行
--      还是 'opencode'，ADD CONSTRAINT 会因校验存量行失败而中断整个升级。
--   3. 反过来，本文件若直接 UPDATE 也会失败——此刻生效的仍是 fork 的旧约束，
--      它只列了 'opencode'，不含 'opencode_go'。所以这里必须**先 DROP 约束再
--      改数据**，随后由上游迁移重新 ADD 回来。
--      channel_monitors / channel_monitor_request_templates 两处上游是带幂等
--      守卫的：约束被 DROP 后 pg_get_constraintdef 返回 NULL，守卫判定为需要
--      重建，因此照样会补回来。
--
-- 全新安装时这里全部是 no-op（没有任何 'opencode' 行，DROP ... IF EXISTS 也安全）。

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;

ALTER TABLE channel_monitors
    DROP CONSTRAINT IF EXISTS channel_monitors_provider_check;

ALTER TABLE channel_monitor_request_templates
    DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_provider_check;

-- 运行期配置：账号与分组。漏掉这两张表会让存量 OpenCode 账号在改名后
-- 落到 default 分支，既不按 CN 多协议转发、也读不到额度。
UPDATE accounts            SET platform        = 'opencode_go' WHERE platform        = 'opencode';
UPDATE groups              SET platform        = 'opencode_go' WHERE platform        = 'opencode';

-- 受 CHECK 约束的四张表。
UPDATE user_platform_quotas SET platform       = 'opencode_go' WHERE platform        = 'opencode';
UPDATE composite_model_routes SET target_platform = 'opencode_go' WHERE target_platform = 'opencode';
UPDATE channel_monitors    SET provider        = 'opencode_go' WHERE provider        = 'opencode';
UPDATE channel_monitor_request_templates SET provider = 'opencode_go' WHERE provider = 'opencode';

-- 历史运维数据：monitor v2 按 platform 聚合渠道健康度，不改名会让同一个平台
-- 的历史与新数据分裂成两行，看板上表现为「旧平台没数据了 / 新平台没有历史」。
UPDATE ops_error_logs      SET platform        = 'opencode_go' WHERE platform        = 'opencode';
