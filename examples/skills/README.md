# runtime/skills 示例

演示如何使用 `pkg/runtime/skills` 注册技能、基于匹配器自动激活、按优先级/互斥组筛选，并手动执行禁用自动激活的技能。无需 API Key，纯本地运行。

## 运行
```bash
go run ./examples/skills \
  -prompt "分析生产日志发现异常 SSH 尝试" \
  -env prod \
  -severity high \
  -channels cli,slack \
  -traits sre,security
```

主要标志：
- `-prompt`：模拟的用户指令文本。
- `-env` / `-severity`：写入 `ActivationContext.Tags`，驱动 `TagMatcher`。
- `-channels`：逗号分隔；`channelMatcher`/`KeywordMatcher` 会用到。
- `-traits`：写入 `ActivationContext.Traits`。
- `-manual-skill`：手动执行的技能名称（默认 `add_note`）。
- `-timeout`：单次技能的超时时间。

## 示例输出（节选）
```
== Activation context ==
prompt: 分析生产日志发现异常 SSH 尝试
tags: map[env:prod severity:high]
channels: [cli slack]
traits: [sre security]

== Auto activation ==
- guardrail (score 0.90, reason tags|require=2)
  output: 已冻结高危指令，env=prod severity=high
- log_summary (score 0.62, reason channel:cli)
  output: 日志概要：分析生产日志发现异常 SSH 尝试（渠道=[cli slack]）

== Manual execution ==
- add_note -> 已记录备注：分析生产日志发现异常 SSH 尝试
```

## 覆盖的核心概念
- `Registry` + `Definition`：注册多个技能，演示优先级、互斥组、禁用自动激活。
- `Handler`/`HandlerFunc`：技能执行逻辑。
- `ActivationContext`：提示、渠道、标签、特征、元数据。
- `Matcher`：`KeywordMatcher`、`TagMatcher`、自定义 `MatcherFunc`、互斥去重。
