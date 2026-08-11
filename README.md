<div align="center">

<img src="assets/logo.svg" alt="Sub2API Logo" width="128" />

# Sub2API

[![Go](https://img.shields.io/badge/Go-1.25.7-00ADD8.svg)](https://golang.org/)
[![Vue](https://img.shields.io/badge/Vue-3.4+-4FC08D.svg)](https://vuejs.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-15+-336791.svg)](https://www.postgresql.org/)
[![Redis](https://img.shields.io/badge/Redis-7+-DC382D.svg)](https://redis.io/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED.svg)](https://www.docker.com/)

**AI API 网关平台 - 订阅配额分发管理**

</div>


## ⚠️ 重要提醒

使用本项目前，请务必仔细阅读以下内容：

- **🚨 服务条款风险**：使用本项目可能违反 Anthropic 等上游服务商的服务条款。请在使用前仔细阅读相关服务商的用户协议，由此产生的一切风险由用户自行承担。
- **⚖️ 合规使用**：请在符合您所在国家或地区法律法规的前提下使用本项目，严禁将其用于任何违法违规用途。
- **📖 免责声明**：本项目仅供技术学习与研究使用，作者不对因使用本项目导致的账户封禁、服务中断、数据丢失或其他任何直接或间接损失承担责任。
- **🚫 无商业授权**：本项目从未授权任何个人或组织基于本项目开展任何形式的商业化运营。任何以本项目名义或基于本项目从事的商业行为均与本项目及其开发者无关，由此产生的一切纠纷、损失和法律责任由行为主体自行承担。

## 项目概述

Sub2API 是一个 AI API 网关平台，用于分发和管理 AI 产品订阅的 API 配额。用户通过平台生成的 API Key 调用上游 AI 服务，平台负责鉴权、计费、负载均衡和请求转发。

## 核心功能

- **多账号管理** - 支持多种上游账号类型（OAuth、API Key）
- **API Key 分发** - 为用户生成和管理 API Key
- **精确计费** - Token 级别的用量追踪和成本计算
- **智能调度** - 智能账号选择，支持粘性会话
- **并发控制** - 用户级和账号级并发限制
- **速率限制** - 可配置的请求和 Token 速率限制
- **内置支付系统** - 支持 EasyPay 易支付、支付宝官方、微信官方、Stripe，用户自助充值，无需独立部署支付服务（[配置指南](docs/PAYMENT_CN.md)）
- **管理后台** - Web 界面进行监控和管理
- **外部系统集成** - 支持通过 iframe 嵌入外部系统（如工单等），扩展管理后台功能

## 技术栈

| 组件 | 技术 |
|------|------|
| 后端 | Go 1.25.7, Gin, Ent |
| 前端 | Vue 3.4+, Vite 5+, TailwindCSS |
| 数据库 | PostgreSQL 15+ |
| 缓存/队列 | Redis 7+ |

---

## Nginx 反向代理注意事项

通过 Nginx 反向代理 Sub2API（或 CRS 服务）并搭配 Codex CLI 使用时，需要在 Nginx 配置的 `http` 块中添加：

```nginx
underscores_in_headers on;
```

Nginx 默认会丢弃名称中含下划线的请求头（如 `session_id`），这会导致多账号环境下的粘性会话功能失效。

---

## 部署方式

### 方式一：脚本安装（推荐）

一键安装脚本，自动从 GitHub Releases 下载预编译的二进制文件。

#### 前置条件

- Linux 服务器（amd64 或 arm64）
- PostgreSQL 15+（已安装并运行）
- Redis 7+（已安装并运行）
- Root 权限

#### 安装步骤

```bash
curl -sSL https://raw.githubusercontent.com/openbmx/sub2api/main/deploy/install.sh | sudo bash
```

脚本会自动：
1. 检测系统架构
2. 下载最新版本
3. 安装二进制文件到 `/opt/sub2api`
4. 创建 systemd 服务
5. 配置系统用户和权限

#### 安装后配置

```bash
# 1. 启动服务
sudo systemctl start sub2api

# 2. 设置开机自启
sudo systemctl enable sub2api

# 3. 在浏览器中打开设置向导
# http://你的服务器IP:8080
```

设置向导将引导你完成：
- 数据库配置
- Redis 配置
- 管理员账号创建

#### 升级

可以直接在 **管理后台** 左上角点击 **检测更新** 按钮进行在线升级。

网页升级功能支持：
- 自动检测新版本
- 一键下载并应用更新
- 支持回滚

#### 常用命令

```bash
# 查看状态
sudo systemctl status sub2api

# 查看日志
sudo journalctl -u sub2api -f

# 重启服务
sudo systemctl restart sub2api

# 卸载
curl -sSL https://raw.githubusercontent.com/openbmx/sub2api/main/deploy/install.sh | sudo bash -s -- uninstall -y
```

---

### 方式二：Docker Compose（推荐）

使用 Docker Compose 部署，包含 PostgreSQL 和 Redis 容器。

#### 前置条件

- Docker 20.10+
- Docker Compose v2+

#### 快速开始（一键部署）

使用自动化部署脚本快速搭建：

```bash
# 创建部署目录
mkdir -p sub2api-deploy && cd sub2api-deploy

# 下载并运行部署准备脚本
curl -sSL https://raw.githubusercontent.com/openbmx/sub2api/main/deploy/docker-deploy.sh | bash

# 启动服务
docker compose up -d

# 查看日志
docker compose logs -f sub2api
```

**脚本功能：**
- 下载 `docker-compose.local.yml`（本地保存为 `docker-compose.yml`）和 `.env.example`
- 自动生成安全凭证（JWT_SECRET、TOTP_ENCRYPTION_KEY、POSTGRES_PASSWORD）
- 创建 `.env` 文件并填充自动生成的密钥
- 创建数据目录（使用本地目录，便于备份和迁移）
- 显示生成的凭证供你记录

#### 手动部署

如果你希望手动配置：

```bash
# 1. 克隆仓库
git clone https://github.com/openbmx/sub2api.git
cd sub2api/deploy

# 2. 复制环境配置文件
cp .env.example .env
chmod 600 .env

# 3. 编辑配置（生成安全密码）
nano .env
```

**`.env` 必须配置项：**

```bash
# PostgreSQL 密码（必需）
POSTGRES_PASSWORD=your_secure_password_here

# JWT 密钥（推荐 - 重启后保持用户登录状态）
JWT_SECRET=your_jwt_secret_here

# TOTP 加密密钥（推荐 - 重启后保留双因素认证）
TOTP_ENCRYPTION_KEY=your_totp_key_here

# 可选：管理员账号
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=your_admin_password

# 可选：自定义端口
SERVER_PORT=8080
```

**生成安全密钥：**
```bash
# 生成 JWT_SECRET
openssl rand -hex 32

# 生成 TOTP_ENCRYPTION_KEY
openssl rand -hex 32

# 生成 POSTGRES_PASSWORD
openssl rand -hex 32
```

```bash
# 4. 创建数据目录（本地版）
mkdir -p data postgres_data redis_data

# 5. 启动所有服务
# 选项 A：本地目录版（推荐 - 易于迁移）
docker compose -f docker-compose.local.yml up -d

# 选项 B：命名卷版（简单设置）
docker compose up -d

# 6. 查看状态
docker compose -f docker-compose.local.yml ps

# 7. 查看日志
docker compose -f docker-compose.local.yml logs -f sub2api
```

#### 部署版本对比

| 版本 | 数据存储 | 迁移便利性 | 适用场景 |
|------|---------|-----------|---------|
| **docker-compose.local.yml** | 本地目录 | ✅ 简单（打包整个目录） | 生产环境、频繁备份 |
| **docker-compose.yml** | 命名卷 | ⚠️ 需要 docker 命令 | 简单设置 |

**推荐：** 使用 `docker-compose.local.yml`（脚本部署）以便更轻松地管理数据。

#### 启用“数据管理”功能（datamanagementd）

如需启用管理后台“数据管理”，需要额外部署宿主机数据管理进程 `datamanagementd`。

关键点：

- 主进程固定探测：`/tmp/sub2api-datamanagement.sock`
- 只有该 Socket 可连通时，数据管理功能才会开启
- Docker 场景需将宿主机 Socket 挂载到容器同路径

详细部署步骤见：`deploy/DATAMANAGEMENTD_CN.md`

#### 访问

在浏览器中打开 `http://你的服务器IP:8080`

如果管理员密码是自动生成的，在日志中查找：
```bash
docker compose -f docker-compose.local.yml logs sub2api | grep "admin password"
```

#### 升级

```bash
# 拉取最新镜像并重建容器
docker compose -f docker-compose.local.yml pull
docker compose -f docker-compose.local.yml up -d
```

#### 轻松迁移（本地目录版）

使用 `docker-compose.local.yml` 时，可以轻松迁移到新服务器：

```bash
# 源服务器
docker compose -f docker-compose.local.yml down
cd ..
tar czf sub2api-complete.tar.gz sub2api-deploy/

# 传输到新服务器
scp sub2api-complete.tar.gz user@new-server:/path/

# 新服务器
tar xzf sub2api-complete.tar.gz
cd sub2api-deploy/
docker compose -f docker-compose.local.yml up -d
```

#### 常用命令

```bash
# 停止所有服务
docker compose -f docker-compose.local.yml down

# 重启
docker compose -f docker-compose.local.yml restart

# 查看所有日志
docker compose -f docker-compose.local.yml logs -f

# 删除所有数据（谨慎！）
docker compose -f docker-compose.local.yml down
rm -rf data/ postgres_data/ redis_data/
```

---

### 方式三：Apple container（macOS）

Apple 芯片 Mac 在 macOS 26 上可使用 Apple `container` 1.1.0 或更高版本运行完整的 Sub2API、PostgreSQL 和 Redis：

```bash
git clone https://github.com/openbmx/sub2api.git
cd sub2api/deploy
./apple-container.sh init
./apple-container.sh up
./apple-container.sh status
```

该方式面向本地开发和人工运维，不提供持续重启监管；生产部署仍推荐 Docker Compose。生命周期命令、持久化、升级和运行时限制见 [deploy/APPLE_CONTAINER.md](deploy/APPLE_CONTAINER.md)。

---

### 方式四：源码编译

从源码编译安装，适合开发或定制需求。

#### 前置条件

- Go 1.21+
- Node.js 18+
- PostgreSQL 15+
- Redis 7+

#### 编译步骤

```bash
# 1. 克隆仓库
git clone https://github.com/openbmx/sub2api.git
cd sub2api

# 2. 安装 pnpm（如果还没有安装）
npm install -g pnpm

# 3. 编译前端
cd frontend
pnpm install
pnpm run build
# 构建产物输出到 ../backend/internal/web/dist/

# 4. 编译后端（嵌入前端）
cd ../backend
VERSION="$(./scripts/resolve-version.sh)"
go build -tags embed -ldflags="-X main.Version=${VERSION}" -o sub2api ./cmd/server

# 5. 创建配置文件
cp ../deploy/config.example.yaml ./config.yaml

# 6. 编辑配置
nano config.yaml
```

> **注意：** `-tags embed` 参数会将前端嵌入到二进制文件中。不使用此参数编译的程序将不包含前端界面。

**`config.yaml` 关键配置：**

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  mode: "release"

database:
  host: "localhost"
  port: 5432
  user: "postgres"
  password: "your_password"
  dbname: "sub2api"

redis:
  host: "localhost"
  port: 6379
  password: ""

jwt:
  secret: "change-this-to-a-secure-random-string"
  expire_hour: 24

default:
  user_concurrency: 5
  user_balance: 0
  api_key_prefix: "sk-"
  rate_multiplier: 1.0
```

### Sora 功能状态（暂不可用）

> ⚠️ 当前 Sora 相关功能因上游接入与媒体链路存在技术问题，暂时不可用。
> 现阶段请勿在生产环境依赖 Sora 能力。
> 文档中的 `gateway.sora_*` 配置仅作预留，待技术问题修复后再恢复可用。

### Sora 媒体签名 URL（功能恢复后可选）

当配置 `gateway.sora_media_signing_key` 且 `gateway.sora_media_signed_url_ttl_seconds > 0` 时，网关会将 Sora 输出的媒体地址改写为临时签名 URL（`/sora/media-signed/...`）。这样无需 API Key 即可在浏览器中直接访问，且具备过期控制与防篡改能力（签名包含 path + query）。

```yaml
gateway:
  # /sora/media 是否强制要求 API Key（默认 false）
  sora_media_require_api_key: false
  # 媒体临时签名密钥（为空则禁用签名）
  sora_media_signing_key: "your-signing-key"
  # 临时签名 URL 有效期（秒）
  sora_media_signed_url_ttl_seconds: 900
```

> 若未配置签名密钥，`/sora/media-signed` 将返回 503。  
> 如需更严格的访问控制，可将 `sora_media_require_api_key` 设为 true，仅允许携带 API Key 的 `/sora/media` 访问。

访问策略说明：
- `/sora/media`：内部调用或客户端携带 API Key 才能下载
- `/sora/media-signed`：外部可访问，但有签名 + 过期控制

`config.yaml` 还支持以下安全相关配置：

- `cors.allowed_origins` 配置 CORS 白名单
- `security.url_allowlist` 配置上游/价格数据/CRS 主机白名单
- `security.url_allowlist.enabled` 可关闭 URL 校验（慎用）
- `security.url_allowlist.allow_insecure_http` 关闭校验时允许 HTTP URL
- `security.url_allowlist.allow_private_hosts` 允许私有/本地 IP 地址
- `security.response_headers.enabled` 可启用可配置响应头过滤（关闭时使用默认白名单）
- `security.csp` 配置 Content-Security-Policy
- `billing.circuit_breaker` 计费异常时 fail-closed
- `security.trust_forwarded_ip_for_api_key_acl` 控制旧版原始转发头接管（为升级兼容默认开启）；关闭后严格使用 `server.trusted_proxies`，其中只应填写直接连接 Sub2API 的精确代理 CIDR
- `security.forwarded_client_ip_headers` 最多配置 16 个第三方 CDN 客户端 IP 请求头；仅在旧版接管开启时按顺序优先于内置请求头解析
- `turnstile.required` 在 release 模式强制启用 Turnstile

自定义客户端 IP 请求头可通过 YAML 配置，也可使用逗号分隔的环境变量：

```bash
SECURITY_FORWARDED_CLIENT_IP_HEADERS=True-Client-IP,X-CDN-Client-IP
```

请求头名称会经过合法性校验、规范化和大小写无关去重。管理员可在安全设置中动态更新列表，无需重启；新安装会持久化 YAML/环境变量默认值，旧安装缺少数据库字段时会自动回填。关闭旧版接管后，自定义头和内置原始转发头均被忽略，只使用 `server.trusted_proxies`。开启接管时必须限制源站仅允许 CDN/代理访问，并确保边缘代理覆盖所有受信客户端 IP 请求头。完整迁移规则和信任边界见 [`deploy/EDGE_SECURITY.md`](deploy/EDGE_SECURITY.md)。

**网关防御纵深建议（重点）**

- `gateway.upstream_response_read_max_bytes`：限制非流式上游响应读取大小（默认 `8MB`），用于防止异常响应导致内存放大。
- `gateway.proxy_probe_response_read_max_bytes`：限制代理探测响应读取大小（默认 `1MB`）。
- `gateway.gemini_debug_response_headers`：默认 `false`，仅在排障时短时开启，避免高频请求日志开销。
- `/auth/register`、`/auth/login`、`/auth/login/2fa`、`/auth/send-verify-code` 已提供服务端兜底限流（Redis 故障时 fail-close）。
- 推荐将 WAF/CDN 作为第一层防护，服务端限流与响应读取上限作为第二层兜底；两层同时保留，避免旁路流量与误配置风险。

**⚠️ 安全警告：HTTP URL 配置**

当 `security.url_allowlist.enabled=false` 时，系统仅执行最小 URL 校验，且**默认允许 HTTP URL**（开发友好模式，Docker Compose 部署的默认值一致）。生产环境建议显式收紧为仅允许 HTTPS：

```yaml
security:
  url_allowlist:
    enabled: false                # 禁用白名单检查
    allow_insecure_http: false    # 仅允许 HTTPS（生产环境推荐）
```

**或通过环境变量：**

```bash
SECURITY_URL_ALLOWLIST_ENABLED=false
SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=false
```

**允许 HTTP 的风险：**
- API 密钥和数据以**明文传输**（可被截获）
- 易受**中间人攻击 (MITM)**
- **不适合生产环境**

**适用场景：**
- ✅ 开发/测试环境的本地服务器（http://localhost）
- ✅ 内网可信端点
- ✅ 获取 HTTPS 前测试账号连通性
- ❌ 生产环境（仅使用 HTTPS）

**设置 `allow_insecure_http: false` 后，HTTP URL 会返回如下错误：**
```
Invalid base URL: invalid url scheme: http
```

如关闭 URL 校验或响应头过滤，请加强网络层防护：
- 出站访问白名单限制上游域名/IP
- 阻断私网/回环/链路本地地址
- 强制仅允许 TLS 出站
- 在反向代理层移除敏感响应头

#### OpenAI Responses WebSocket 准入限制

`gateway.openai_ws` 限制面向客户端的 Responses WebSocket 会话的存活时长与总数。这些防护独立于按轮次分配的用户与账号并发槽位——后者在每轮结束后即释放。

```yaml
gateway:
  openai_ws:
    # 接收并解压客户端首条消息的总时长
    client_first_message_timeout_seconds: 30
    # 关闭在两轮之间空闲的客户端连接；置 0 关闭该防护
    ingress_inter_turn_idle_timeout_seconds: 300
    # 单个 API Key 的活跃入站会话分布式上限；置 0 关闭
    max_ingress_connections_per_api_key: 64
```

首消息超时是一个总读取截止时间。若部署需要接受超大上下文或图像密集的请求，且客户端链路较慢，可上调至 120–300 秒。该超时在 HTTP bridge 路由之前生效，因此 bridge 模式不会绕过此限制。

连接数上限通过 Redis 协调，使用 60 秒租约、每 20 秒续租一次。若某个进程在一个完整租约周期内都无法确认租约，它会主动关闭本地 WebSocket，而不是脱离全局上限继续服务。

在选择 `http_bridge` 等账号级 WS 模式之前，需先启用 v2 模式路由：

```yaml
gateway:
  openai_ws:
    mode_router_v2_enabled: true
```

也可通过环境变量 `GATEWAY_OPENAI_WS_MODE_ROUTER_V2_ENABLED=true` 设置。`http_bridge` 用于「客户端 WebSocket + 上游 HTTP」的运行方式，适合灰度上线或规避上游 WebSocket 问题。

#### ⚠️ 重要：创建管理员账号

初始管理员账号**只能通过 setup 向导创建**（首次启动时访问 `http://<host>:8080`）。`config.yaml` 中的 `default.admin_email` / `default.admin_password` 字段**不会被用来创建管理员**——它们只是出于历史原因保留在模板里。

由于上面第 5 步预先创建了 `config.yaml`，**setup 向导在首次启动时会被跳过**：服务检测到 config 已存在，会直接进入正常模式，此时 `users` 表为空，首次登录会返回 `invalid email or password`。

**创建管理员的两种方式：**

1. **推荐——让向导自动生成 `config.yaml`：** 跳过上面的第 5 步（不要执行 `cp`）。直接运行 `./sub2api`，访问 `http://localhost:8080`，向导会引导你完成数据库、Redis 和管理员账号配置，并自动写出 `config.yaml`。

2. **如果你已经创建了 `config.yaml`：** 首次启动前先把它临时移走以触发向导，完成后再恢复：
   ```bash
   mv config.yaml config.yaml.bak
   ./sub2api        # 向导在 http://localhost:8080 启动，并生成新的 config.yaml
   # 向导完成后 Ctrl+C 停服，再恢复你的配置：
   mv config.yaml.bak config.yaml
   ./sub2api        # 重启进入正常模式，用刚创建的管理员登录
   ```

```bash
# 6. 运行应用
./sub2api
```

#### HTTP/2 (h2c) 与 HTTP/1.1 回退

后端明文端口默认支持 h2c，并保留 HTTP/1.1 回退用于 WebSocket 与旧客户端。浏览器通常不支持 h2c，性能收益主要在反向代理或内网链路。

**反向代理示例（Caddy）：**

```caddyfile
transport http {
	versions h2c h1
}
```

**验证：**

```bash
# h2c prior knowledge
curl --http2-prior-knowledge -I http://localhost:8080/health
# HTTP/1.1 回退
curl --http1.1 -I http://localhost:8080/health
# WebSocket 回退验证（需管理员 token）
websocat -H="Sec-WebSocket-Protocol: sub2api-admin, jwt.<ADMIN_TOKEN>" ws://localhost:8080/api/v1/admin/ops/ws/qps
```

#### 开发模式

```bash
# 后端（支持热重载）
cd backend
go run ./cmd/server

# 前端（支持热重载）
cd frontend
pnpm run dev
```

#### 代码生成

修改 `backend/ent/schema` 后，需要重新生成 Ent + Wire：

```bash
cd backend
go generate ./ent
go generate ./cmd/server
```

---

## 简易模式

简易模式适合个人开发者或内部团队快速使用，不依赖完整 SaaS 功能。

- 启用方式：设置环境变量 `RUN_MODE=simple`
- 功能差异：隐藏 SaaS 相关功能，跳过计费流程
- 安全注意事项：生产环境需同时设置 `SIMPLE_MODE_CONFIRM=true` 才允许启动

---

## 异步图像任务

耗时较长的 OpenAI / Grok 图像生成与编辑，可以通过 `/v1/images/generations/async` 或 `/v1/images/edits/async` 提交，随后在 `/v1/images/tasks/{task_id}` 轮询结果，无需一直占用 CDN 连接。请求与响应示例见 [异步图像任务](docs/ASYNC_IMAGE_TASKS.md)。

---

## Grok / xAI 支持

Sub2API 同时支持通过 xAI OAuth 接入的 Grok 订阅账号，以及标准的 xAI API Key 账号。两种账号类型都会把 OpenAI 兼容的 Responses 流量转发到 xAI。

### 支持范围

- 平台名：`grok`
- 账号类型：OAuth 订阅账号、xAI API Key 账号
- 对外 Responses 端点：`/v1/responses`、`/responses`、`/backend-api/codex/responses`。OAuth 账号转发至 Grok 订阅代理，API Key 账号转发至 `https://api.x.ai/v1/responses`
- 对外 Claude 兼容端点：`/v1/messages`，转换为 xAI Responses 后再以 Anthropic Messages 格式返回，供 Claude CLI 类客户端使用
- 对外 Chat Completions 端点：`/v1/chat/completions`、`/chat/completions`，按账号类型转发到对应的 xAI 上游
- Responses 端点接受 Codex CLI 风格的 WebSocket 准入，并桥接到 xAI 的 HTTP/SSE Responses 上游
- 文本模型：`grok-4.5`、`grok-4.3`、`grok-build-0.1`、`grok-composer-2.5-fast`、`grok-4.20-0309-reasoning`、`grok-4.20-0309-non-reasoning`、`grok-4.20-multi-agent-0309`
- Grok 分组的媒体端点：`/v1/images/generations`、`/images/generations`、`/v1/images/edits`、`/images/edits`、`/v1/videos/generations`、`/videos/generations`、`/v1/videos/edits`、`/videos/edits`、`/v1/videos/extensions`、`/videos/extensions`、`/v1/videos/{request_id}`、`/videos/{request_id}`。生成、编辑和续拍请求需要分组具备图像生成权限
- 媒体模型：`grok-imagine`、`grok-imagine-image-quality`、`grok-imagine-image`、`grok-imagine-edit`、`grok-imagine-video`、`grok-imagine-video-1.5`
- JSON 格式的图像编辑与视频生成请求，可在 `image`、`images`、`reference_images`、`mask` 对象中携带图像引用。xAI 兼容载荷请使用 `url` 字段；旧版 `image_url` 字段仍被接受，转发前会归一化为 `url`
- 不在本平台支持范围内：TTS、语音转写、浏览器自动化、cookies、Grok 网页抓取

### OAuth 配置

Grok OAuth 流程基于 PKCE，无需在仓库中提交任何私密凭据。默认客户端参数沿用兼容客户端所使用的公开 xAI OAuth 流程，且每一项都可通过环境变量覆盖：

| 变量 | 默认值 |
|------|--------|
| `XAI_OAUTH_CLIENT_ID` | 公开的 xAI OAuth client ID |
| `XAI_OAUTH_SCOPE` | `openid profile email offline_access grok-cli:access api:access` |
| `XAI_OAUTH_REDIRECT_URI` | `http://127.0.0.1:56121/callback` |
| `XAI_OAUTH_AUTHORIZE_URL` | `https://auth.x.ai/oauth2/authorize` |
| `XAI_OAUTH_TOKEN_URL` | `https://auth.x.ai/oauth2/token` |
| `XAI_BASE_URL` | `https://api.x.ai/v1`；仅用于运行时诊断覆盖（实际请求转发由账号的 `base_url` 决定） |
| `XAI_GROK_CLI_VERSION` | `0.2.114`；可选覆盖发送给 `cli-chat-proxy.grok.com` 的客户端标识。该值同时是下限，低于它的覆盖会被丢弃 |

管理员可在后台创建 Grok OAuth 账号或 API Key 账号。OAuth 授权与重新授权也可通过管理端接口完成：

| 接口 | 用途 |
|------|------|
| `POST /api/v1/admin/grok/oauth/auth-url` | 生成 xAI OAuth 授权 URL |
| `POST /api/v1/admin/grok/oauth/exchange-code` | 用回调 URL、query string 或 code 换取 OAuth 凭据 |
| `POST /api/v1/admin/grok/oauth/refresh-token` | 校验或刷新 Grok refresh token |
| `POST /api/v1/admin/grok/accounts/:id/refresh` | 刷新已有的 Grok 账号 |

OAuth 凭据复用账号已有的 JSON 字段存储：`access_token`、`refresh_token`、`token_type`、`expires_at`、`base_url`，以及可选的 `email`、`subscription_tier` 和 `entitlement_status`。OAuth 推断的默认上游是 `https://cli-chat-proxy.grok.com/v1`；此前存了旧默认值 `https://api.x.ai/v1` 的 OAuth 账号，运行时会被重定向到订阅代理。显式配置的自定义上游不受影响。

API Key 账号请在创建账号对话框中选择 **Grok → API Key**。官方 base URL 默认为 `https://api.x.ai/v1`，凭据沿用账号已有的 `base_url` 和 `api_key` 字段。OAuth 账号仍走上面的订阅流程。

### Grok Build CLI 配置

1. 在 Sub2API 管理后台添加一个 `grok` OAuth 账号并完成 xAI 授权，或添加一个 Grok API Key 账号。
2. 创建 Grok 分组，把账号挂到分组下，再创建一个归属该分组的 Sub2API API Key。
3. 在用户 API Key 页面点击 **使用密钥**，选择 **Grok CLI**。弹窗会针对 macOS/Linux 或 Windows 生成对应的配置文件和 base URL，**OpenCode** 标签页还提供 OpenCode 配置。
4. 如需手动配置，将下面内容保存为 `~/.grok/config.toml`（Windows：`%USERPROFILE%\.grok\config.toml`）：

```toml
[models]
default = "grok"
web_search = "grok"

[model."grok"]
model = "grok-4.5"
base_url = "https://your-sub2api.example.com/v1"
name = "Grok 4.5"
api_key = "sk-your-sub2api-key"
api_backend = "responses"
context_window = 1000000
supports_backend_search = true
```

合并配置前请先备份已有的 `config.toml`。该文件内含 Sub2API API Key，请妥善保管并在支持的系统上收紧文件权限。随后可校验生效配置并做一次冒烟请求：

```bash
grok inspect
grok -p "Reply with sub2api-ok" -m grok
```

上面的 `base_url` 是以 `/v1` 结尾的 Sub2API 公网地址，既不是 `api.x.ai`，也不是内部的 xAI OAuth 代理地址。

### 用量与配额展示

xAI 的配额是被动采集的。Sub2API 不会凭空构造订阅配额数值，只在 xAI 于成功或被限流的上游响应中返回白名单内的 rate-limit 响应头时才记录。在拿到第一个可用的上游响应之前，后台会把配额显示为未知，但仍会展示 Sub2API 本地的用量统计。

`401` 响应会把凭据失效的账号临时移出调度。`403` 响应按访问权限或权益问题处理，而不是触发 token 刷新循环。`429` 响应会依据 `Retry-After` 或一个较短的冷却时间把账号临时移出调度。

新的 Grok 图像与视频生成请求会走一套媒体专用的准入校验。API Key 账号始终具备资格；OAuth 账号则需要 xAI 计费探测给出明确的付费权益证据——Free、被拒、缺失、格式异常和结论不明确的计费观测结果，都会被排除在新的媒体生成之外。尚未探测过的 OAuth 账号会在第一个媒体请求转发前先行探测，导入账号时也会主动执行「计费优先」的配额探测。聊天请求和视频状态查询不受该媒体隔离影响。若没有任何具备资格的账号，媒体端点返回 HTTP `503`，错误类型为 `grok_media_no_eligible_account`。

管理员可通过账号创建/更新接口覆盖自动媒体准入：把 `extra.grok_media_eligible` 设为 `false` 表示排除，设为 `true` 表示强制具备资格。更新时设为 `null` 可移除覆盖、回到基于探测的自动行为；不传该字段则保持当前覆盖不变。仅有周度额度周期不会被视为付费层级信号。成功的图像响应必须至少包含一个真实的图像输出；空的 HTTP `200` 响应会触发账号故障转移，而不会被计费并作为成功生成返回。

---

## Antigravity 使用说明

Sub2API 支持 [Antigravity](https://antigravity.so/) 账户，授权后可通过专用端点访问 Claude 和 Gemini 模型。

### 专用端点

| 端点 | 模型 |
|------|------|
| `/antigravity/v1/messages` | Claude 模型 |
| `/antigravity/v1beta/` | Gemini 模型 |

### Claude Code 配置示例

```bash
export ANTHROPIC_BASE_URL="http://localhost:8080/antigravity"
export ANTHROPIC_AUTH_TOKEN="sk-xxx"
```

### 混合调度模式

Antigravity 账户支持可选的**混合调度**功能。开启后，通用端点 `/v1/messages` 和 `/v1beta/` 也会调度该账户。

> **⚠️ 注意**：Anthropic Claude 和 Antigravity Claude **不能在同一上下文中混合使用**，请通过分组功能做好隔离。

---

## 项目结构

```
sub2api/
├── backend/                  # Go 后端服务
│   ├── cmd/server/           # 应用入口
│   ├── internal/             # 内部模块
│   │   ├── config/           # 配置管理
│   │   ├── model/            # 数据模型
│   │   ├── service/          # 业务逻辑
│   │   ├── handler/          # HTTP 处理器
│   │   └── gateway/          # API 网关核心
│   └── resources/            # 静态资源
│
├── frontend/                 # Vue 3 前端
│   └── src/
│       ├── api/              # API 调用
│       ├── stores/           # 状态管理
│       ├── views/            # 页面组件
│       └── components/       # 通用组件
│
└── deploy/                   # 部署文件
    ├── docker-compose.yml    # Docker Compose 配置
    ├── .env.example          # Docker Compose 环境变量
    ├── config.example.yaml   # 二进制部署完整配置文件
    └── install.sh            # 一键安装脚本
```

---

## 许可证

本项目基于 [GNU 宽通用公共许可证 v3.0](LICENSE)（或更高版本）授权。

Copyright (c) 2026 Wesley Liddick
