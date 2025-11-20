# Slash Commands Example

演示 `pkg/runtime/commands` 的核心用法：
- 使用 `Parse` 从多行输入中提取斜杠命令，支持引号、转义、`--flag` 与 `--flag=value` 两种格式。
- 读取 `Invocation.Args` 与 `Invocation.Flag()` 处理位置参数和布尔/键值标志。
- 通过 `Executor` 注册处理函数，按顺序执行解析好的命令并返回 `Result`。

## 运行

```bash
go run ./examples/commands
```

示例输入脚本内置于 `main.go`，包含多种命令形式：
- `/deploy staging --version 2025.11.20 --region=us-east-1 --force`
- `/query "latency p95" --since "2025-11-20 08:00" --limit=3`
- `/note add "release checklist" "/tmp/release plan.md" --tag "ops crew" --private`
- `/backup run --path=/var/log/app --dest "./tmp/log backup" --compress`

输出分两段：
1) **Parsed Invocations**：逐行展示解析结果（命令名、位置参数、标志，含 `Position` 与原始行 `Raw`）。
2) **Execution**：`Executor` 按顺序调用处理函数，展示 `Output` 与 `Metadata`。其中布尔标志无显式值时默认为 `true`。

无需 API Key 或外部服务，适合快速理解 slash command 解析与分发流程。
