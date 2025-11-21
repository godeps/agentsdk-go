# 实现 Subagents Loader 模块

## 目标文件
pkg/runtime/subagents/loader.go

## 功能需求

1. 从文件系统自动加载 subagents
2. 扫描目录：
   - 项目级: {projectRoot}/.claude/agents/*.md
   - 用户级: ~/.claude/agents/*.md

3. 文件格式解析：
   - YAML frontmatter（必需）:
     - name: subagent 标识符（必需，小写字母+数字+连字符）
     - description: 用途说明（必需）
     - tools: 工具列表（可选，逗号分隔）
     - model: 模型别名（可选：sonnet/opus/haiku/inherit）
     - permissionMode: 权限模式（可选：default/acceptEdits/bypassPermissions/plan/ignore）
     - skills: 技能列表（可选，逗号分隔）
   - Markdown body: System prompt 内容

4. 生成 Handler：
   - 返回 []SubagentRegistration（需要在 pkg/api/types.go 中定义）
   - Handler 逻辑：返回 system prompt 内容
   - 简单实现即可

5. 优先级规则（三层）：
   - 项目级 > CLI 定义 > 用户级
   - 同名 subagent 时项目级覆盖用户级

6. 错误处理：
   - 部分文件加载失败不影响其他文件
   - 返回加载错误列表供调用方记录

## API 设计

```go
package subagents

import (
    "context"
)

type LoaderOptions struct {
    ProjectRoot string
    UserHome    string
    EnableUser  bool  // 是否加载 ~/.claude/
}

type SubagentFile struct {
    Name     string
    Path     string
    Metadata SubagentMetadata
    Body     string
}

type SubagentMetadata struct {
    Name           string `yaml:"name"`
    Description    string `yaml:"description"`
    Tools          string `yaml:"tools"`           // 逗号分隔
    Model          string `yaml:"model"`
    PermissionMode string `yaml:"permissionMode"`
    Skills         string `yaml:"skills"`          // 逗号分隔
}

type SubagentRegistration struct {
    Definition Definition
    Handler    Handler
}

// LoadFromFS 从文件系统加载 subagents 定义
// 返回：subagent 注册列表和加载错误列表（非致命）
func LoadFromFS(opts LoaderOptions) ([]SubagentRegistration, []error)
```

## 实现要点

- 使用 filepath.Walk 扫描 *.md 文件
- 文件名（去掉 .md）作为备用 name
- 验证 name 字段：小写字母+数字+连字符
- 项目级覆盖同名的用户级（map 合并）
- 解析逗号分隔的列表（tools, skills）
- YAML frontmatter 格式:
  ```yaml
  ---
  name: agent-identifier
  description: agent purpose
  tools: tool1, tool2
  model: sonnet
  permissionMode: default
  skills: skill1, skill2
  ---
  System prompt content...
  ```

## Handler 实现

生成的 Handler 应该：
- 接收 Context
- 返回 Result{Output: systemPrompt}
- 简单实现：返回 markdown body 内容

## 参考现有代码

- @pkg/runtime/subagents/manager.go - Manager 和 Definition 结构
- @pkg/runtime/subagents/context.go - Context 结构
- @pkg/runtime/commands/loader.go - 已实现的 Commands Loader（参考结构）
- @pkg/runtime/skills/loader.go - 已实现的 Skills Loader（参考结构）

## 测试要求

创建 loader_test.go：
- TestLoadFromFS_Basic: 基本加载功能
- TestLoadFromFS_Priority: 项目级覆盖用户级
- TestLoadFromFS_YAML: YAML frontmatter 解析
- TestLoadFromFS_Metadata: 元数据解析（tools, model, skills）
- TestLoadFromFS_Errors: 错误处理
- 使用 t.TempDir() 创建临时测试文件

## 注意事项

1. SubagentRegistration 类型可能已在 pkg/api/types.go 中定义，需要检查
2. 保持代码简洁，单文件不超过 500 行
3. 遵循 KISS 原则
4. 优先级处理：使用 map，项目级覆盖用户级
