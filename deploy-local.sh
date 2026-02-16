#!/bin/bash

# Business2API 本地 Docker 部署脚本
# 皇上吉祥，吾皇万岁万岁万万岁！

set -e

echo "================================"
echo "  Business2API 本地部署脚本"
echo "================================"
echo ""

# 检查 Docker 是否安装
if ! command -v docker &> /dev/null; then
    echo "❌ 错误: 未检测到 Docker，请先安装 Docker"
    exit 1
fi

echo "✅ Docker 已安装"
echo ""

# 检查配置文件
if [ ! -f "config.json" ]; then
    echo "❌ 错误: 配置文件 config.json 不存在"
    exit 1
fi

echo "✅ 配置文件已准备"
echo ""

# 创建数据目录
if [ ! -d "data" ]; then
    mkdir -p data
    echo "✅ 已创建数据目录"
fi

echo ""
echo "开始构建并启动服务..."
echo ""

# 停止旧容器（如果存在）
docker compose -f docker/docker-compose.yml down 2>/dev/null || true

# 启动服务
docker compose -f docker/docker-compose.yml up -d --build

echo ""
echo "================================"
echo "  部署完成！"
echo "================================"
echo ""
echo "📊 服务状态:"
docker compose -f docker/docker-compose.yml ps
echo ""
echo "🔗 访问地址:"
echo "  - API 服务: http://localhost:8000"
echo "  - 健康检查: http://localhost:8000/health"
echo "  - 模型列表: http://localhost:8000/v1/models"
echo ""
echo "🔑 API Key: sk-local-test-key-123456"
echo ""
echo "📝 常用命令:"
echo "  查看日志:   docker compose -f docker/docker-compose.yml logs -f"
echo "  停止服务:   docker compose -f docker/docker-compose.yml down"
echo "  重启服务:   docker compose -f docker/docker-compose.yml restart"
echo "  查看状态:   docker compose -f docker/docker-compose.yml ps"
echo ""
