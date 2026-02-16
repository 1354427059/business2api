#!/bin/bash

# Business2API 测试脚本
# 皇上吉祥，吾皇万岁万岁万万岁！

API_KEY="${API_KEY:-sk-local-test-key-123456}"
BASE_URL="http://localhost:8000"
WORKER_ID="${REGISTRAR_WORKER_ID:-manual-checker-$$}"

echo "================================"
echo "  Business2API 功能测试"
echo "================================"
echo ""

# 1. 健康检查
echo "1️⃣  健康检查..."
curl -s $BASE_URL/health | python3 -m json.tool
echo ""
echo ""

# 2. 获取模型列表
echo "2️⃣  获取模型列表..."
curl -s $BASE_URL/v1/models \
  -H "Authorization: Bearer $API_KEY" | python3 -m json.tool | head -30
echo ""
echo ""

# 3. 查看账号池状态
echo "3️⃣  账号池状态..."
STATUS_BEFORE=$(curl -s $BASE_URL/admin/status \
  -H "Authorization: Bearer $API_KEY")
echo "$STATUS_BEFORE" | python3 -m json.tool
echo ""
echo ""

# 3.1 外部续期闭环观测（manual + registrar）
echo "3️⃣  外部续期闭环观测..."
echo "前置条件: external_refresh_mode=true，且 registrar 服务已启动"
echo "步骤提示: 先制造 401（如使用过期 authorization 调 /v1/chat/completions）观察 pending_external 增加"
PENDING_EXTERNAL_BEFORE=$(echo "$STATUS_BEFORE" | python3 - <<'PY'
import json,sys
try:
    data=json.load(sys.stdin)
    print(int(data.get("pending_external",0)))
except Exception:
    print(-1)
PY
)
echo "pending_external(before): $PENDING_EXTERNAL_BEFORE"
echo ""
echo "3.1.1 claim 外部续期任务..."
CLAIM_RESP=$(curl -s "$BASE_URL/admin/registrar/refresh-tasks/claim" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d "{\"worker_id\":\"$WORKER_ID\",\"limit\":5,\"lease_sec\":180}")
echo "$CLAIM_RESP" | python3 -m json.tool
echo ""
echo "3.1.2 registrar 执行 refresh 并 upload 后，检查状态与指标..."
echo "提示: 若 claim count > 0，可执行: docker compose -f docker/docker-compose.yml logs -f registrar"
echo "提示: 告警事件可通过以下命令筛选"
echo "      docker compose -f docker/docker-compose.yml logs --tail=300 business2api | grep ALERT_"
sleep 2
STATUS_AFTER=$(curl -s $BASE_URL/admin/status \
  -H "Authorization: Bearer $API_KEY")
METRICS_RESP=$(curl -s "$BASE_URL/admin/registrar/metrics" \
  -H "Authorization: Bearer $API_KEY")
echo "status(after):"
echo "$STATUS_AFTER" | python3 -m json.tool
echo "registrar metrics:"
echo "$METRICS_RESP" | python3 -m json.tool
echo ""
echo ""

# 4. 测试聊天补全（如果没有可用账号可能会失败）
echo "4️⃣  测试聊天补全..."
RESPONSE=$(curl -s -w "\n%{http_code}" $BASE_URL/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": false
  }')

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -eq 200 ]; then
    echo "✅ 聊天成功："
    echo "$BODY" | python3 -m json.tool
elif [ "$HTTP_CODE" -eq 503 ]; then
    echo "⚠️  暂无可用账号（正在自动注册中...）"
    echo "提示：系统正在自动注册 Gemini Business 账号，请稍后再试"
else
    echo "❌ 请求失败 (HTTP $HTTP_CODE)"
    echo "$BODY"
fi
echo ""
echo ""

echo "================================"
echo "  测试完成！"
echo "================================"
echo ""
echo "💡 提示:"
echo "  - 如果账号池为空，系统会自动注册新账号"
echo "  - 注册过程需要几分钟时间"
echo "  - 可以使用以下命令查看注册进度:"
echo "    docker compose -f docker/docker-compose.yml logs -f"
echo ""
