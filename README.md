# Business2API

> OpenAI / Gemini / Claude 兼容的 Gemini Business API 代理服务，支持账号池管理、自动注册、Flow 图片/视频生成、管理面板与外部 registrar 续期闭环。

[![Build](https://github.com/XxxXTeam/business2api/actions/workflows/build.yml/badge.svg)](https://github.com/XxxXTeam/business2api/actions/workflows/build.yml)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://golang.org)

## 项目运行流程（已按当前代码校准）

当前程序入口位于 `main.go`，实际启动逻辑如下：

1. 加载 `config/config.json`（不存在时自动创建默认配置）
2. 应用环境变量覆盖（例如 `API_KEYS`、`POOL_SERVER_SECRET`）
3. 按 `pool_server.mode` 进入运行模式：
   - `local`：API 服务 + 本地账号池（默认）
   - `server`：API 服务 + 号池调度中心 + WS 任务分发
   - `client`：仅作为工作节点连接 server，不提供 API
4. 若开启 `flow.enable=true`，初始化 Flow Token 池（`data/at/*.txt`）
5. 若配置了 API Key，则 `/v1/*` 与 `/admin/*` 开启鉴权

## 功能特性

- 多协议兼容：
  - OpenAI：`/v1/chat/completions`、`/v1/models`
  - Claude：`/v1/messages`
  - Gemini：`/v1beta/models`、`/v1beta/models/*action`
- 账号池能力：自动注册、轮询调度、401/403 处理、冷却控制
- 外部续期闭环：支持 Python registrar claim/fail/metrics
- Flow 生成：图片与视频模型（含 T2V / I2V / R2V）
- 管理面板：账号/文件管理、日志流、在线测试
- 配置热重载：监听 `config/config.json`
- 代理池：订阅 + 文件源 + 健康检查

## 支持模型

### 基础模型（始终可见）

- `gemini-2.5-flash`
- `gemini-2.5-pro`
- `gemini-3-pro-preview`
- `gemini-3-pro`
- `gemini-3-flash-preview`
- `gemini-3-flash`
- `gemini-2.5-flash-preview-latest`

### 功能后缀模型（基础模型扩展）

- `-image`：图片生成
- `-video`：视频生成
- `-search`：联网搜索
- 支持混合后缀：如 `gemini-2.5-flash-image-search`

说明：代码支持无后缀和后缀模型并存；后缀会触发对应工具能力。

### Flow 模型（仅在 `flow.enable=true` 时可见）

- 图片：
  - `gemini-2.5-flash-image-landscape/portrait`
  - `gemini-3.0-pro-image-landscape/portrait`
  - `imagen-4.0-generate-preview-landscape/portrait`
- 视频：
  - `veo_3_1_t2v_fast_landscape/portrait`
  - `veo_2_1_fast_d_15_t2v_landscape/portrait`
  - `veo_2_0_t2v_landscape/portrait`
  - `veo_3_1_i2v_s_fast_fl_landscape/portrait`
  - `veo_2_1_fast_d_15_i2v_landscape/portrait`
  - `veo_2_0_i2v_landscape/portrait`
  - `veo_3_0_r2v_fast_landscape/portrait`

## 快速开始

### 方式一：仓库内 Docker Compose（推荐）

> 当前 `docker/docker-compose.yml` 使用 `build.context: ..`，必须在仓库目录运行，不适用于“只下载 compose 文件”场景。

```bash
# 1) 克隆仓库
git clone https://github.com/XxxXTeam/business2api.git
cd business2api

# 2) 准备配置文件（程序读取 config/config.json）
cp config/config.json.example config/config.json

# 3) 准备数据目录
mkdir -p data data/registrar-artifacts

# 4) 如需鉴权，先设置 API_KEYS（逗号分隔）
export API_KEYS="sk-your-api-key"

# 5) 如需启用 python registrar，再设置：
# export B2A_API_KEY="sk-your-api-key"
# export MAIL_KEY="your-mail-key"

# 6) 启动
# 全部服务：business2api + registrar + selenium
docker compose -f docker/docker-compose.yml up -d --build
```

验证：

```bash
curl http://localhost:8000/health
curl http://localhost:8000/v1/models -H "Authorization: Bearer sk-your-api-key"
```

### 方式二：Docker Run（镜像运行）

```bash
docker pull ghcr.io/xxxteam/business2api:latest

mkdir -p config data
# 拉取示例配置并写入 config/config.json
curl -fsSL \
  https://raw.githubusercontent.com/XxxXTeam/business2api/master/config/config.json.example \
  -o config/config.json

docker run -d \
  --name business2api \
  -p 8000:8000 \
  -v "$(pwd)/data:/app/data" \
  -v "$(pwd)/config/config.json:/app/config/config.json:ro" \
  -e API_KEYS="sk-your-api-key" \
  ghcr.io/xxxteam/business2api:latest
```

### 方式三：源码本地运行

```bash
# 需要 Go 1.25+
go mod download
go run .

# 调试模式
go run . --debug
```

命令行参数：

- `--debug` / `-d`：注册调试模式（截图输出到 `data/screenshots/`）
- `--auto`：自动订阅代理模式
- `--refresh [email]`：有头浏览器刷新账号后退出
- `--help` / `-h`

## 配置说明

主配置文件：`config/config.json`

> 程序不会默认读取仓库根目录 `config.json`。

示例（与 `config/config.json.example` 对齐）：

```json
{
  "api_keys": [],
  "listen_addr": ":8000",
  "data_dir": "./data",
  "default_config": "",
  "debug": false,
  "pool": {
    "target_count": 50,
    "min_count": 10,
    "check_interval_minutes": 30,
    "enable_go_register": true,
    "register_threads": 1,
    "register_headless": true,
    "mail_channel_order": ["chatgpt"],
    "duckmail_bearer": "",
    "refresh_on_startup": true,
    "refresh_cooldown_sec": 240,
    "use_cooldown_sec": 15,
    "max_fail_count": 3,
    "enable_browser_refresh": true,
    "browser_refresh_headless": true,
    "browser_refresh_max_retry": 1,
    "external_refresh_mode": false,
    "registrar_base_url": "http://127.0.0.1:8090"
  },
  "pool_server": {
    "enable": false,
    "mode": "local",
    "server_addr": "http://server-ip:8000",
    "listen_addr": ":8000",
    "secret": "",
    "target_count": 50,
    "client_threads": 2,
    "data_dir": "./data",
    "expired_action": "delete"
  },
  "proxy_pool": {
    "subscribes": ["http://example.com/s/example"],
    "files": [],
    "health_check": true,
    "check_on_startup": true
  },
  "flow": {
    "enable": false,
    "tokens": [],
    "timeout": 120,
    "poll_interval": 3,
    "max_poll_attempts": 500
  }
}
```

### 环境变量覆盖

Go 服务：

- `LISTEN_ADDR`
- `DATA_DIR`
- `PROXY`
- `CONFIG_ID`
- `API_KEYS`（覆盖配置中的 `api_keys`，逗号分隔）
- `API_KEY`（追加单个 key）
- `POOL_SERVER_SECRET`
- `DUCKMAIL_BEARER`

Python registrar：

- `B2A_BASE_URL`（默认 `http://business2api:8000`）
- `B2A_API_KEY`
- `MAIL_API`
- `MAIL_KEY`
- `SELENIUM_REMOTE_URL`
- `WORKER_ID`
- `REFRESH_TASK_LEASE_SEC`
- `CREDENTIALS_WAIT_TIMEOUT_SEC`

### 热重载

服务会监听 `config/config.json` 的写入变更。典型可热更新项：

- `api_keys`
- `debug`
- `pool.refresh_cooldown_sec`
- `pool.use_cooldown_sec`
- `pool.max_fail_count`
- `pool.enable_browser_refresh`
- `pool.browser_refresh_headless`
- `pool.browser_refresh_max_retry`
- `pool.auto_delete_401`
- `pool.enable_go_register`
- `pool.external_refresh_mode`
- `pool.mail_channel_order`
- `pool.duckmail_bearer`
- `pool.registrar_base_url`

手动触发：

```bash
curl -X POST http://localhost:8000/admin/reload-config \
  -H "Authorization: Bearer sk-your-api-key"
```

## 运行模式与架构

### Local（默认）

- 单进程内运行 API + 账号池
- 可选 Go 内置注册
- 可选外部 registrar 续期

### Server

- 运行 API + 账号池调度中心
- 暴露 `/ws` 给 Client 工作节点
- 通过 `/pool/upload-account` 接收客户端上传结果

### Client

- 不提供 `/v1/*` API
- 作为工作节点通过 WS 接收注册/续期任务

## 管理面板

访问：`/admin/panel`

- 默认账号：`admin`
- 默认密码：`admin123`
- 密码文件：`data/admin_panel_auth.json`
- 会话 cookie：`b2a_admin_session`（默认 TTL 12 小时）

说明：`/admin/*` 支持两种鉴权方式：

1. API Key（`Authorization: Bearer ...`）
2. 面板登录会话（cookie）

## API 使用示例

### 获取模型列表

```bash
curl http://localhost:8000/v1/models \
  -H "Authorization: Bearer sk-your-api-key"
```

### 聊天补全

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-api-key" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

### 多模态（图片输入）

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-api-key" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "描述这张图片"},
        {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,..."}}
      ]
    }]
  }'
```

## Flow Token 使用

启用前提：`flow.enable=true`

### 推荐方式：文件注入

```bash
mkdir -p data/at
echo "your-cookie-string" > data/at/account1.txt
```

程序会从 `data/at` 提取 `__Secure-next-auth.session-token` 并自动加载。

### 管理接口方式

```bash
curl -X POST http://localhost:8000/admin/flow/add-token \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"cookie":"your-cookie-string"}'
```

## API 端点总览

### 公开端点

- `GET /`
- `GET /health`
- `GET /admin/panel`
- `GET /admin/panel/assets/*filepath`
- `POST /admin/panel/login`
- `POST /admin/panel/logout`
- `GET /admin/panel/me`
- `POST /admin/panel/change-password`（需登录会话）
- `GET /ws`（仅 server 模式）

### 业务端点（API Key）

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/messages`
- `GET /v1beta/models`
- `GET /v1beta/models/:model`
- `POST /v1beta/models/*action`
- `POST /v1/models/*action`

### 管理端点（API Key 或 Session）

- `POST /admin/register`
- `POST /admin/refresh`
- `GET /admin/status`
- `GET /admin/stats`
- `GET /admin/ip`
- `POST /admin/force-refresh`
- `POST /admin/reload-config`
- `POST /admin/config/cooldown`
- `POST /admin/browser-refresh`
- `POST /admin/config/browser-refresh`
- `GET /admin/accounts`
- `GET /admin/pool-files`
- `GET /admin/pool-files/export`
- `POST /admin/pool-files/import`
- `POST /admin/pool-files/delete-invalid/preview`
- `POST /admin/pool-files/delete-invalid/execute`
- `GET /admin/logs/stream`
- `POST /admin/registrar/upload-account`
- `GET /admin/registrar/refresh-tasks`（兼容旧版）
- `POST /admin/registrar/refresh-tasks/claim`
- `POST /admin/registrar/refresh-tasks/fail`
- `GET /admin/registrar/metrics`
- `POST /admin/registrar/trigger-register`
- `GET /admin/flow/status`
- `POST /admin/flow/add-token`
- `POST /admin/flow/remove-token`
- `POST /admin/flow/reload`

### 内部端点（Pool Secret）

- `POST /pool/upload-account`

当 `pool_server.secret` 非空时，需携带请求头：`X-Pool-Secret`。

## Python Registrar（外部注册/续期）

目录：`python/registrar`

对外接口：

- `GET /health`
- `GET /metrics`
- `POST /trigger/register?count=1`
- `POST /trigger/refresh?limit=20`
- `GET /logs`

在 Go 服务中通过 `pool.registrar_base_url` 配置 registrar 地址，管理端可用：

- `POST /admin/registrar/trigger-register`
- `GET /admin/logs/stream?source=registrar`

## 开发与测试

### 本地测试

```bash
# 运行单元测试
go test ./...

# 仅跑管理端相关测试
go test -run TestAdmin ./...

# 快速接口冒烟
./test-api.sh
```

### 构建

```bash
go build -o business2api .
go build -tags "with_quic with_utls" -o business2api .
```

## 项目结构

```text
.
├── main.go
├── config/
│   ├── config.json
│   ├── config.json.example
│   └── README.md
├── src/
│   ├── adminauth/
│   ├── adminlogs/
│   ├── flow/
│   ├── logger/
│   ├── pool/
│   ├── proxy/
│   ├── register/
│   └── utils/
├── python/registrar/
├── web/admin/
├── docker/docker-compose.yml
└── test-api.sh
```

## 常见问题

### 1) 为什么改了根目录 `config.json` 不生效？

程序读取的是 `config/config.json`，请修改该文件。

### 2) 为什么“只下载 docker-compose.yml”后启动失败？

仓库中的 `docker/docker-compose.yml` 依赖仓库源码进行构建（`build.context: ..`），需在仓库目录运行。

### 3) 为什么调用 `/v1/chat/completions` 返回 503？

通常是账号池暂无可用账号，可查看：

```bash
curl http://localhost:8000/admin/status -H "Authorization: Bearer sk-your-api-key"
```

### 4) 管理面板如何重置密码？

删除 `data/admin_panel_auth.json` 后重启，系统会重建默认账号 `admin/admin123`。

## License

MIT
