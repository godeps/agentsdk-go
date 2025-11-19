package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/cexll/agentsdk-go/pkg/api"
	"github.com/cexll/agentsdk-go/pkg/middleware"
	modelpkg "github.com/cexll/agentsdk-go/pkg/model"
	"github.com/google/uuid"
)

// 交互式 REPL 示例，复用固定会话 ID 保持历史。
func main() {
	provider := &modelpkg.AnthropicProvider{ModelName: "claude-sonnet-4-5-20250929"}

	// 创建 debug middleware
	traceMW := middleware.NewTraceMiddleware(".trace")

	rt, err := api.New(context.Background(), api.Options{
		EntryPoint:   api.EntryPointCLI,
		ModelFactory: provider,
		Middleware:   []middleware.Middleware{traceMW},
	})
	if err != nil {
		log.Fatalf("build runtime: %v", err)
	}
	defer rt.Close()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("You> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "exit" {
			break
		}
		if line == "" {
			continue
		}

		req := api.Request{
			Prompt:    line,
			SessionID: uuid.NewString(),
			Mode: api.ModeContext{
				EntryPoint: api.EntryPointCLI,
				CLI:        &api.CLIContext{User: os.Getenv("USER")},
			},
		}
		resp, err := rt.Run(context.Background(), req)
		if err != nil {
			log.Fatalf("run: %v", err)
		}
		if resp.Result == nil {
			continue
		}

		fmt.Printf("\nAssistant> %s\n\n", resp.Result.Output)
		for _, call := range resp.Result.ToolCalls {
			params, err := json.MarshalIndent(call.Arguments, "", "  ")
			if err != nil {
				log.Printf("marshal tool params: %v", err)
				continue
			}
			fmt.Printf("Tool %s params: %s\n", call.Name, string(params))
		}
	}
}
