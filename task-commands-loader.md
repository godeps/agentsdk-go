# 实现 Commands Loader 模块

## 目标文件
pkg/runtime/commands/loader.go

## 功能需求

1. 从文件系统自动加载 slash commands
2. 扫描目录：
   - 项目级: {projectRoot}/.claude/commands/*.md
   - 用户级: ~/.claude/commands/*.md
   - 支持子目录命名空间（如 frontend/deploy.md）

3. 文件格式解析：
   - YAML frontmatter（可选）:
     - description: 命令描述
     - allowed-tools: 工具白名单
     - argument-hint: 参数提示
     - model: 模型名称
     - disable-model-invocation: 是否禁用模型调用
   - Markdown body: 命令指令内容

4. 生成 Handler：
   - 返回 []CommandRegistration（需要在 pkg/api/types.go 中定义）
   - Handler 逻辑：返回 markdown body 作为 prompt（简单实现，返回 Result{Output: body}）
   - 支持参数替换（$ARGUMENTS, $1, $2 等）

5. 优先级规则：
   - 项目级优先于用户级
   - 同名命令时项目级覆盖用户级

6. 错误处理：
   - 部分文件加载失败不影响其他文件
   - 返回加载错误列表供调用方记录

## API 设计

```go
package commands

import (
    "context"
)

type LoaderOptions struct {
    ProjectRoot string
    UserHome    string
    EnableUser  bool  // 是否加载 ~/.claude/
}

type CommandFile struct {
    Name     string
    Path     string
    Metadata CommandMetadata
    Body     string
}

type CommandMetadata struct {
    Description            string `yaml:"description"`
    AllowedTools          string `yaml:"allowed-tools"`
    ArgumentHint          string `yaml:"argument-hint"`
    Model                 string `yaml:"model"`
    DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

type CommandRegistration struct {
    Definition Definition
    Handler    Handler
}

// LoadFromFS 从文件系统加载命令定义
// 返回：命令注册列表和加载错误列表（非致命）
func LoadFromFS(opts LoaderOptions) ([]CommandRegistration, []error)
```

## 实现要点

- 使用 filepath.Walk 或 os.ReadDir 扫描目录
- 使用标准库解析 YAML frontmatter（或 gopkg.in/yaml.v3）
- 处理文件不存在、权限错误等异常
- 命令名称从文件名提取（去掉 .md 后缀，转小写）
- 子目录只影响组织方式，不影响命令名
- YAML frontmatter 格式:
  ```
  ---
  description: xxx
  ---
  body content
  ```

## Handler 实现

生成的 Handler 应该：
- 接收 Invocation（包含 Args, Flags）
- 替换 body 中的变量：
  - $ARGUMENTS -> 所有参数拼接
  - $1, $2, $3... -> 对应位置的参数
- 返回 Result{Output: processedBody}

## 参考现有代码

- @pkg/runtime/commands/executor.go - Executor 和 Definition 结构
- @pkg/runtime/commands/parser.go - 命令名称验证逻辑
- @pkg/api/runtime_helpers.go:119-130 - 现有 registerCommands 函数

## 测试要求

创建 loader_test.go：
- TestLoadFromFS_Basic: 基本加载功能
- TestLoadFromFS_Priority: 项目级覆盖用户级
- TestLoadFromFS_YAML: YAML frontmatter 解析
- TestLoadFromFS_Errors: 错误处理
- 使用 t.TempDir() 创建临时测试文件

## 注意事项

1. CommandRegistration 类型需要在某个地方定义（建议在 pkg/api/types.go 或直接在 loader.go 中）
2. 如果 pkg/api 中已有 CommandRegistration，直接复用
3. 保持代码简洁，单文件不超过 500 行
4. 遵循 KISS 原则
