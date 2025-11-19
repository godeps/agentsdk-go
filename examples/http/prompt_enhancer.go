package main

import (
	"strings"
)

// enhancePrompt 智能增强用户提示词，确保模型正确理解工具调用意图
// 特别针对简单命令输入（如 "ls"）自动包装成明确的工具调用指令
func enhancePrompt(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return input
	}

	// 如果已经包含明确的指令词，直接返回
	keywords := []string{"execute", "run", "use", "call", "tool", "bash", "command"}
	lowerInput := strings.ToLower(input)
	for _, kw := range keywords {
		if strings.Contains(lowerInput, kw) {
			return input
		}
	}

	// 检查是否看起来像 bash 命令
	if looksLikeBashCommand(input) {
		// 包装成明确的工具调用指令
		return "Execute this bash command using the bash_execute tool with the 'command' parameter set to: " + input
	}

	// 对于其他查询，保持原样
	return input
}

// looksLikeBashCommand 判断输入是否看起来像 bash 命令
func looksLikeBashCommand(input string) bool {
	if len(input) == 0 {
		return false
	}

	// 常见 bash 命令列表
	commonCommands := []string{
		"ls", "pwd", "cd", "cat", "echo", "grep", "find", "head", "tail",
		"mkdir", "touch", "cp", "mv", "rm", "chmod", "chown",
		"ps", "top", "kill", "df", "du", "free", "uname",
		"git", "npm", "go", "python", "node", "docker", "curl", "wget",
	}

	words := strings.Fields(input)
	if len(words) == 0 {
		return false
	}

	firstWord := words[0]

	// 检查第一个词是否是常见命令
	for _, cmd := range commonCommands {
		if firstWord == cmd || strings.HasPrefix(firstWord, cmd) {
			return true
		}
	}

	// 检查是否包含路径或文件名（不含空格）
	if !strings.Contains(input, " ") {
		if strings.Contains(input, "/") || (strings.Contains(input, ".") && !strings.HasPrefix(input, ".")) {
			return true
		}
	}

	// 检查是否包含管道、重定向等 shell 特殊字符
	shellChars := []string{"|", ">", "<", ">>", "&&", "||"}
	for _, char := range shellChars {
		if strings.Contains(input, char) {
			return true
		}
	}

	return false
}
