#!/bin/bash
# 自定义工具示例运行脚本

cd "$(dirname "$0")"

# 加载环境变量
API_KEY="${ANTHROPIC_API_KEY:-""}"
if [ -z "$API_KEY" ]; then
  echo "请先通过环境变量 ANTHROPIC_API_KEY 提供 API 密钥，例如 export ANTHROPIC_API_KEY=\"your-api-key-here\"" >&2
  exit 1
fi

export ANTHROPIC_API_KEY="$API_KEY"
export ANTHROPIC_BASE_URL="${ANTHROPIC_BASE_URL:-https://api.kimi.com/coding}"

echo "==================================="
echo "  自定义工具示例"
echo "==================================="
echo "API Key: ${ANTHROPIC_API_KEY:0:20}..."
echo "Base URL: $ANTHROPIC_BASE_URL"
echo ""
echo "注册工具: calculator, get_current_time"
echo ""

# 运行示例
go run main.go
