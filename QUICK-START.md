# 🎉 Business2API 本地部署成功！

> 皇上吉祥，吾皇万岁万岁万万岁！

## ✅ 部署状态

- **服务状态**: 🟢 运行中
- **访问地址**: http://localhost:8000
- **API Key**: `sk-local-test-key-123456`
- **容器状态**: healthy

---

## 🚀 快速开始

### 1. 测试 API 连接

```bash
# 健康检查
curl http://localhost:8000/health

# 获取模型列表
curl http://localhost:8000/v1/models \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

### 2. 发送聊天请求

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-local-test-key-123456" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [
      {"role": "user", "content": "你好"}
    ],
    "stream": false
  }'
```

---

## ⚠️ 首次使用提示

**当前状态**: 系统正在自动注册 Gemini Business 账号

- ⏳ 注册过程需要 2-5 分钟
- 🔄 可以查看注册日志：
  ```bash
  docker compose -f docker/docker-compose.yml logs -f
  ```
- ✅ 注册完成后会自动有可用账号

**查看账号池状态**:
```bash
curl http://localhost:8000/admin/status \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

---

## 📚 详细文档

- **完整部署指南**: `DOCKER-DEPLOY-GUIDE.md`
- **官方文档**: `README.md`
- **测试脚本**: `./test-api.sh`

---

## 🛠️ 常用命令

```bash
# 查看日志
docker compose -f docker/docker-compose.yml logs -f

# 重启服务
docker compose -f docker/docker-compose.yml restart

# 停止服务
docker compose -f docker/docker-compose.yml down

# 查看状态
docker compose -f docker/docker-compose.yml ps

# 运行测试
./test-api.sh
```

---

## 🎯 支持的模型

### 文本模型
- `gemini-2.5-flash` ⚡ 快速
- `gemini-2.5-pro` 🧠 强大
- `gemini-3-pro-preview` 🔮 预览版
- `gemini-3-flash` ⚡ 新版本

### 图片生成
- `gemini-2.5-flash-image-landscape` 横版
- `gemini-2.5-flash-image-portrait` 竖版

### 视频生成
- `veo_3_1_t2v_fast_landscape` 文生视频
- `veo_3_1_i2v_s_fast_fl_landscape` 图生视频

### 功能后缀
- `-image` 启用图片生成
- `-video` 启用视频生成
- `-search` 启用联网搜索
- 可以组合使用，如 `gemini-2.5-flash-image-search`

---

## 🔌 集成到应用

### ChatGPT Next Web
```bash
OPENAI_API_KEY=sk-local-test-key-123456
OPENAI_API_BASE_URL=http://localhost:8000/v1
```

### Lobe Chat
```bash
OPENAI_API_KEY=sk-local-test-key-123456
OPENAI_PROXY_URL=http://localhost:8000/v1
```

### Python 代码示例
```python
from openai import OpenAI

client = OpenAI(
    api_key="sk-local-test-key-123456",
    base_url="http://localhost:8000/v1"
)

response = client.chat.completions.create(
    model="gemini-2.5-flash",
    messages=[
        {"role": "user", "content": "你好"}
    ]
)

print(response.choices[0].message.content)
```

---

## 📊 监控接口

### 账号池状态
```bash
curl http://localhost:8000/admin/status \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

### API 统计
```bash
curl http://localhost:8000/admin/stats \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

### 手动刷新账号
```bash
curl -X POST http://localhost:8000/admin/refresh \
  -H "Authorization: Bearer sk-local-test-key-123456"
```

---

## ❓ 常见问题

### 1. 提示"没有可用账号"
**原因**: 账号池为空，系统正在自动注册

**解决**: 等待 2-5 分钟，系统会自动完成注册

### 2. 如何修改 API Key？
编辑 `config.json` 文件中的 `api_keys` 字段，然后重启服务

### 3. 如何启用代理？
编辑 `config.json` 文件，配置 `proxy_pool` 字段

---

## 🎉 开始使用

现在您可以：

1. ✅ 等待账号注册完成（2-5 分钟）
2. ✅ 使用测试脚本测试功能：`./test-api.sh`
3. ✅ 集成到您的应用中
4. ✅ 查看详细文档了解更多功能

**祝您使用愉快！** 🚀
