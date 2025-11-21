# 实现 Skills Loader 模块

## 目标文件
pkg/runtime/skills/loader.go

## 功能需求

1. 从文件系统自动加载 skills
2. 扫描目录：
   - 项目级: {projectRoot}/.claude/skills/*/SKILL.md
   - 用户级: ~/.claude/skills/*/SKILL.md
   - 插件级: 暂不实现（预留接口）

3. 文件格式解析：
   - YAML frontmatter（必需）:
     - name: skill 标识符（必需，小写字母+数字+连字符，最多64字符）
     - description: 技能描述（必需，最多1024字符）
     - allowed-tools: 工具白名单（可选）
   - Markdown body: 技能详细说明

4. 支持文件（按需加载）：
   - skill-name/SKILL.md（必需）
   - skill-name/reference.md（可选）
   - skill-name/examples.md（可选）
   - skill-name/scripts/（可选）
   - skill-name/templates/（可选）

5. 生成 Handler：
   - 返回 []SkillRegistration（需要在 pkg/api/types.go 中定义）
   - Handler 逻辑：返回 skill body + 支持文件内容
   - 支持按需加载（第一版可以全部加载，后续优化）

6. 优先级规则：
   - 项目级、用户级、插件级共存（无覆盖）
   - 同名 skill 时记录 warning

7. 错误处理：
   - 部分文件加载失败不影响其他文件
   - 返回加载错误列表供调用方记录

## API 设计

```go
package skills

import (
    "context"
)

type LoaderOptions struct {
    ProjectRoot string
    UserHome    string
    EnableUser  bool  // 是否加载 ~/.claude/
}

type SkillFile struct {
    Name          string
    Path          string
    Metadata      SkillMetadata
    Body          string
    SupportFiles  map[string]string  // 支持文件内容（reference.md, examples.md 等）
}

type SkillMetadata struct {
    Name         string `yaml:"name"`
    Description  string `yaml:"description"`
    AllowedTools string `yaml:"allowed-tools"`
}

type SkillRegistration struct {
    Definition Definition
    Handler    Handler
}

// LoadFromFS 从文件系统加载 skills 定义
// 返回：skill 注册列表和加载错误列表（非致命）
func LoadFromFS(opts LoaderOptions) ([]SkillRegistration, []error)
```

## 实现要点

- 使用 filepath.Walk 扫描 */SKILL.md 文件
- 目录名作为 skill-name
- 验证 name 字段：小写字母+数字+连字符，最多64字符
- 验证 description 字段：最多1024字符
- 检查支持文件（reference.md, examples.md）
- 合并项目级 + 用户级（同名时记录 warning）
- YAML frontmatter 格式:
  ```yaml
  ---
  name: skill-identifier
  description: skill description
  ---
  body content
  ```

## Handler 实现

生成的 Handler 应该：
- 接收 ActivationContext（包含 Prompt, Tags, Channels 等）
- 返回 Result{Output: skillBody + supportFiles}
- 简单实现：返回 SKILL.md body 内容

## 参考现有代码

- @pkg/runtime/skills/registry.go - Registry 和 Definition 结构
- @pkg/runtime/skills/matcher.go - ActivationContext 结构
- @pkg/runtime/commands/loader.go - 已实现的 Commands Loader（参考结构）

## 测试要求

创建 loader_test.go：
- TestLoadFromFS_Basic: 基本加载功能
- TestLoadFromFS_Merge: 项目级 + 用户级合并
- TestLoadFromFS_YAML: YAML frontmatter 解析
- TestLoadFromFS_SupportFiles: 支持文件加载
- TestLoadFromFS_Errors: 错误处理
- 使用 t.TempDir() 创建临时测试文件

## 注意事项

1. SkillRegistration 类型可能已在 pkg/api/types.go 中定义，需要检查
2. 保持代码简洁，单文件不超过 500 行
3. 遵循 KISS 原则
4. 第一版可以预加载所有支持文件，不必实现按需加载
