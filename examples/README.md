# agentsdk-go Examples

仅保留可用的 3 个示例，均可在仓库根目录运行。

**修复说明**
- Sandbox 资源限制（CPU/内存/磁盘）默认生效，避免工具过度消耗。
- historyStore 使用 LRU，`MaxSessions` 默认 500，防止长时间运行造成内存泄漏。

## cli
最小化 CLI 运行流程，直接调用 Anthropic Provider；如果当前目录或仓库缺少 `.claude/config.yaml`，示例会自动生成一个临时配置并清理，避免 `config version is required` 错误。

```bash
export ANTHROPIC_API_KEY=sk-...
go run ./examples/cli
```

环境变量：
- 必需：`ANTHROPIC_API_KEY`
- 可选：`AGENTSDK_PROJECT_ROOT`（指向已有 `.claude` 的目录时跳过临时配置）、`ANTHROPIC_BASE_URL`（代理/镜像）、`HTTP_PROXY`/`HTTPS_PROXY`

## http
标准库实现的 HTTP API（/v1/run、/v1/run/stream、/v1/tools/execute），默认依赖 Sandbox 与 LRU Session 限制。

```bash
export ANTHROPIC_API_KEY=sk-...
export AGENTSDK_HTTP_ADDR=":8080"           # 可选
export AGENTSDK_MAX_SESSIONS=500            # LRU 上限，防止内存泄漏
curl -s http://localhost:8080/health || true
```

核心环境变量：
- 模型：`ANTHROPIC_API_KEY`（必需），`ANTHROPIC_BASE_URL`（代理/镜像可选）
- 基础：`AGENTSDK_HTTP_ADDR`，`AGENTSDK_PROJECT_ROOT`，`AGENTSDK_SANDBOX_ROOT`，`AGENTSDK_MODEL`
- 网络：`AGENTSDK_NETWORK_ALLOW`（逗号分隔白名单，默认 `api.anthropic.com`）
- 超时：`AGENTSDK_DEFAULT_TIMEOUT`，`AGENTSDK_MAX_TIMEOUT`
- 资源：`AGENTSDK_RESOURCE_CPU_PERCENT`，`AGENTSDK_RESOURCE_MEMORY_MB`，`AGENTSDK_RESOURCE_DISK_MB`，`AGENTSDK_MAX_BODY_BYTES`，`AGENTSDK_MAX_SESSIONS`

## mcp
演示通过 stdio 连接 `mcp-server-time` 并调用 MCP 工具。

```bash
uv tool install mcp-server-time  # 如未安装
uvx mcp-server-time --help       # 验证可用
go run ./examples/mcp
```

环境要求：`uv`/`uvx` 在 PATH 中，无需 API 密钥。
