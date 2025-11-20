# Subagents example

演示 `pkg/runtime/subagents` 的最小用法：注册子代理、基于 matcher 自动选择、以及手动派发请求。

## 运行
```bash
go run ./examples/subagents                      # 根据指令自动选择子代理
go run ./examples/subagents -target plan         # 强制派发到指定子代理
go run ./examples/subagents -prompt "scan logs"  # 触发 explore 路径
```
无需 API Key；程序会打印内置定义、已注册子代理，然后执行一次 `Manager.Dispatch` 并输出 `Result`。

## 代码要点
- `BuiltinDefinitions`：列出 general-purpose / explore / plan 的默认模型与上下文。
- `Manager.Register`：把 Definition 与 Handler 绑定，包含自定义的 `deploy_guard` 示例（优先级+互斥锁）。
- `Matchers`：通过 `KeywordMatcher` / `TagMatcher` 自动匹配合适子代理，缺省回落到 general-purpose。
- `Context`：在派发时合并默认 BaseContext、请求元数据、工具白名单，并在 Handler 中读取模型/工具列表。
- `Request` / `Result`：演示如何传入指令、元数据、工具限制，以及返回输出与元数据。 
