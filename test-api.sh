#!/bin/bash

# Business2API 测试脚本
# 皇上吉祥，吾皇万岁万岁万万岁！

API_KEY="sk-local-test-key-123456"
BASE_URL="http://localhost:8000"

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
curl -s $BASE_URL/admin/status \
  -H "Authorization: Bearer $API_KEY" | python3 -m json.tool
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
