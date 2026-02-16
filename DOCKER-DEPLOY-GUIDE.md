# Business2API 本地 Docker 部署指南

> 皇上吉祥，吾皇万岁万岁万万岁！

## 📋 项目概述

Business2API 是一个 OpenAI/Gemini 兼容的 Gemini Business API 代理服务，主要功能包括：

- ✅ **多 API 兼容**：支持 OpenAI、Gemini、Claude 格式
- 🏊 **智能账号池**：自动管理 Gemini Business 账号
- 🎨 **多模态支持**：图片/视频输入和生成
- 🌊 **流式响应**：支持 SSE 流式输出
- 🤖 **自动注册**：浏览器自动化注册新账号

---

## 🚀 快速部署

### 前置要求

- ✅ Docker Desktop（Mac/Windows）或 Docker Engine（Linux）
- ✅ 至少 4GB 可用内存
- ✅ 至少 10GB 磁盘空间

### 一键部署

```bash
# 1. 给部署脚本添加执行权限
chmod +x deploy-local.sh

# 2. 执行部署脚本
./deploy-local.sh
```

### 手动部署步骤

如果需要手动部署，执行以下命令：

```bash
# 1. 创建数据目录
mkdir -p data

# 2. 启动服务
docker compose -f docker/docker-compose.yml up -d --build

# 3. 查看日志
docker compose -f docker/docker-compose.yml logs -f
```

---

## ⚙️ 配置说明

### 核心配置项

配置文件：`config.json`

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `api_keys` | API 访问密钥列表 | `["sk-local-test-key-123456"]` |
| `listen_addr` | 监听地址 | `:8000` |
| `data_dir` | 账号数据存储目录 | `./data` |
| `debug` | 调试模式 | `true` |
| `pool.target_count` | 目标账号数量 | `10` |
| `pool.min_count` | 最小账号数 | `3` |

### 修改配置后重启

配置文件支持热重载，大部分配置项无需重启：

```bash
# 手动触发配置重载
curl -X POST http://localhost:8000/admin/reload-config \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

---

## 🔌 API 使用示例

### 1. 健康检查

```bash
curl http://localhost:8000/health
```

### 2. 获取模型列表

```bash
curl http://localhost:8000/v1/models \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

### 3. 聊天补全（非流式）

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-local-test-key-123456" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [
      {"role": "user", "content": "你好，请介绍一下你自己"}
    ],
    "stream": false
  }'
```

### 4. 聊天补全（流式）

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-local-test-key-123456" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [
      {"role": "user", "content": "写一首关于春天的诗"}
    ],
    "stream": true
  }'
```

### 5. 多模态（图片输入）

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-local-test-key-123456" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "描述这张图片"},
          {
            "type": "image_url",
            "image_url": {
              "url": "data:image/jpeg;base64,/9j/4AAQSkZJRg..."
            }
          }
        ]
      }
    ]
  }'
```

### 6. 图片生成

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-local-test-key-123456" \
  -d '{
    "model": "gemini-2.5-flash-image-landscape",
    "messages": [
      {"role": "user", "content": "一只可爱的橘猫在阳光下睡觉"}
    ],
    "stream": true
  }'
```

---

## 🎯 管理接口

### 查看账号池状态

```bash
curl http://localhost:8000/admin/status \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

### 查看详细统计

```bash
curl http://localhost:8000/admin/stats \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

### 刷新账号池

```bash
curl -X POST http://localhost:8000/admin/refresh \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

---

## 🐳 Docker 常用命令

### 查看服务状态

```bash
docker compose -f docker/docker-compose.yml ps
```

### 查看实时日志

```bash
docker compose -f docker/docker-compose.yml logs -f
```

### 重启服务

```bash
docker compose -f docker/docker-compose.yml restart
```

### 停止服务

```bash
docker compose -f docker/docker-compose.yml down
```

### 进入容器

```bash
docker exec -it business2api sh
```

---

## 🔧 高级配置

### 启用代理池

编辑 `config.json`：

```json
{
  "proxy_pool": {
    "subscribes": [
      "http://your-proxy-subscribe-url"
    ],
    "files": [],
    "health_check": true,
    "check_on_startup": true
  }
}
```

### 启用 Flow 图片/视频生成

1. 获取 Flow Token：
   - 访问 https://labs.google/fx 并登录
   - 打开开发者工具 → Application → Cookies
   - 复制所有 cookie

2. 配置 Flow：

```json
{
  "flow": {
    "enable": true,
    "tokens": ["your-cookie-string"],
    "timeout": 120,
    "poll_interval": 3,
    "max_poll_attempts": 500
  }
}
```

3. 重启服务：

```bash
docker compose -f docker/docker-compose.yml restart
```

### 添加更多 API Key

编辑 `config.json`：

```json
{
  "api_keys": [
    "sk-key-1",
    "sk-key-2",
    "sk-key-3"
  ]
}
```

---

## 🌐 在其他应用中使用

### ChatGPT Next Web

```bash
# 环境变量
OPENAI_API_KEY=sk-local-test-key-123456
OPENAI_API_BASE_URL=http://localhost:8000/v1
```

### Lobe Chat

```bash
# 环境变量
OPENAI_API_KEY=sk-local-test-key-123456
OPENAI_PROXY_URL=http://localhost:8000/v1
```

### Open WebUI

设置 → 模型 → OpenAI API：
- API Key: `sk-local-test-key-123456`
- Base URL: `http://localhost:8000/v1`

---

## 📊 支持的模型列表

### 文本模型
- `gemini-2.5-flash` ✅
- `gemini-2.5-pro` ✅
- `gemini-3-pro-preview` ✅
- `gemini-3-flash-preview` ✅
- `gemini-3-flash` ✅

### 图片生成模型
- `gemini-2.5-flash-image-landscape` 横版图片
- `gemini-2.5-flash-image-portrait` 竖版图片

### 视频生成模型
- `veo_3_1_t2v_fast_landscape` 文生视频
- `veo_3_1_i2v_s_fast_fl_landscape` 图生视频

### 功能后缀
- `-image`: 只启用图片生成
- `-video`: 只启用视频生成
- `-search`: 只启用联网搜索
- 混合后缀: `gemini-2.5-flash-image-search`

---

## ❓ 常见问题

### 1. 服务启动失败

**检查端口占用：**
```bash
lsof -i :8000
```

**解决方案：**
```bash
# 停止占用端口的进程
kill -9 <PID>

# 或修改 docker-compose.yml 中的端口映射
```

### 2. 无法访问 API

**检查容器状态：**
```bash
docker compose -f docker/docker-compose.yml ps
```

**查看日志：**
```bash
docker compose -f docker/docker-compose.yml logs
```

### 3. 401 Unauthorized

**原因：** API Key 不正确

**解决方案：**
- 确认请求头中的 Authorization 值为 `Bearer sk-local-test-key-123456`
- 检查 `config.json` 中的 `api_keys` 配置

### 4. 503 Service Unavailable

**原因：** 无可用账号

**解决方案：**
```bash
# 查看账号状态
curl http://localhost:8000/admin/status \
  -H "Authorization: Bearer sk-local-test-key-123456"

# 手动刷新账号
curl -X POST http://localhost:8000/admin/refresh \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

### 5. 浏览器注册失败

**原因：** 容器内缺少 Chrome 或内存不足

**解决方案：**
- 增加 Docker 内存限制（建议至少 4GB）
- 检查是否安装了 Chromium：`docker exec -it business2api which chromium`

---

## 🔄 更新服务

```bash
# 1. 停止服务
docker compose -f docker/docker-compose.yml down

# 2. 拉取最新镜像
docker pull ghcr.io/xxxteam/business2api:latest

# 3. 重新启动
docker compose -f docker/docker-compose.yml up -d
```

---

## 📝 数据备份

```bash
# 备份数据目录
tar -czf business2api-data-$(date +%Y%m%d).tar.gz data/

# 恢复数据
tar -xzf business2api-data-20260215.tar.gz
```

---

## 🛡️ 安全建议

1. **修改默认 API Key**：将 `sk-local-test-key-123456` 改为强密码
2. **限制访问**：如果只在本地使用，不要暴露到公网
3. **定期备份**：定期备份 `data` 目录
4. **监控日志**：定期检查异常访问日志

---

## 📞 获取帮助

- **GitHub 仓库**: https://github.com/XxxXTeam/business2api
- **问题反馈**: https://github.com/XxxXTeam/business2api/issues
- **在线 Demo**: https://business2api.openel.top

---

**祝您使用愉快！如有问题，请查看日志或提交 Issue。**
