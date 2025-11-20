package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cexll/agentsdk-go/pkg/runtime/skills"
	"github.com/cexll/agentsdk-go/pkg/runtime/subagents"
)

type runConfig struct {
	prompt  string
	target  string
	timeout time.Duration
}

func main() {
	cfg := parseConfig()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	mgr := subagents.NewManager()

	registerBuiltin(mgr, subagents.TypeGeneralPurpose, nil, generalPurposeHandler)
	registerBuiltin(mgr, subagents.TypeExplore, []skills.Matcher{
		skills.KeywordMatcher{Any: []string{"log", "trace", "grep", "read"}},
		skills.TraitMatcher{Traits: []string{"fast"}},
	}, exploreHandler)
	registerBuiltin(mgr, subagents.TypePlan, []skills.Matcher{
		skills.KeywordMatcher{Any: []string{"plan", "roadmap", "步骤", "拆解"}},
	}, planHandler)
	registerDeployGuard(mgr)

	printBuiltinCatalog()
	printRegistered(mgr)

	req := subagents.Request{
		Target:        cfg.target,
		Instruction:   cfg.prompt,
		Activation:    buildActivation(cfg),
		ToolWhitelist: []string{"glob", "read"},
		Metadata: map[string]any{
			"request_id": "subagents-demo",
			"owner":      "agentsdk-go",
		},
	}

	res, err := mgr.Dispatch(ctx, req)
	if err != nil {
		log.Fatalf("dispatch failed: %v", err)
	}

	fmt.Println("\n== Result ==")
	fmt.Printf("subagent: %s\n", res.Subagent)
	fmt.Printf("output: %v\n", res.Output)
	if len(res.Metadata) > 0 {
		fmt.Printf("metadata: %v\n", res.Metadata)
	}
	if res.Error != "" {
		fmt.Printf("error: %s\n", res.Error)
	}
}

func parseConfig() runConfig {
	cfg := runConfig{}
	flag.StringVar(&cfg.prompt, "prompt", "plan a production deploy and inspect recent errors", "instruction text sent to subagents")
	flag.StringVar(&cfg.target, "target", "", "force a subagent name (empty = auto match)")
	flag.DurationVar(&cfg.timeout, "timeout", 1500*time.Millisecond, "dispatch timeout")
	flag.Parse()
	cfg.prompt = strings.TrimSpace(cfg.prompt)
	if cfg.prompt == "" {
		cfg.prompt = "describe incident timeline"
	}
	return cfg
}

func buildActivation(cfg runConfig) skills.ActivationContext {
	return skills.ActivationContext{
		Prompt: cfg.prompt,
		Channels: []string{
			"cli",
		},
		Tags: map[string]string{
			"env": "prod",
		},
		Traits: []string{"fast"},
		Metadata: map[string]any{
			"source": "examples/subagents",
		},
	}
}

func registerBuiltin(mgr *subagents.Manager, name string, matchers []skills.Matcher, handler subagents.HandlerFunc) {
	def, ok := subagents.BuiltinDefinition(name)
	if !ok {
		log.Fatalf("builtin %s missing", name)
	}
	if len(matchers) > 0 {
		def.Matchers = matchers
	}
	if err := mgr.Register(def, handler); err != nil {
		log.Fatalf("register %s: %v", name, err)
	}
}

func registerDeployGuard(mgr *subagents.Manager) {
	def := subagents.Definition{
		Name:        "deploy_guard",
		Description: "Blocks risky production deploys before handing off to planning/ops agents.",
		Priority:    5,
		MutexKey:    "ops",
		BaseContext: subagents.Context{
			Model:         subagents.ModelHaiku,
			ToolWhitelist: []string{"bash", "read"},
			Metadata:      map[string]any{"role": "sre"},
		},
		Matchers: []skills.Matcher{
			skills.KeywordMatcher{Any: []string{"deploy", "上线", "发布", "rollout"}},
			skills.TagMatcher{Require: map[string]string{"env": "prod"}},
		},
	}
	if err := mgr.Register(def, subagents.HandlerFunc(deployGuardHandler)); err != nil {
		log.Fatalf("register deploy_guard: %v", err)
	}
}

func generalPurposeHandler(ctx context.Context, subCtx subagents.Context, req subagents.Request) (subagents.Result, error) {
	if err := ctx.Err(); err != nil {
		return subagents.Result{}, err
	}
	model := preferModel(subCtx, subagents.ModelSonnet)
	summary := fmt.Sprintf("general-purpose agent (%s) handling: %s", model, req.Instruction)
	return subagents.Result{
		Output: summary,
		Metadata: map[string]any{
			"model": model,
			"tools": subCtx.ToolList(),
		},
	}, nil
}

func exploreHandler(ctx context.Context, subCtx subagents.Context, req subagents.Request) (subagents.Result, error) {
	if err := ctx.Err(); err != nil {
		return subagents.Result{}, err
	}
	tools := subCtx.ToolList()
	joined := strings.Join(tools, ", ")
	if joined == "" {
		joined = "(no tool limits)"
	}
	output := fmt.Sprintf("explore agent scanning code paths for '%s' using tools: %s", req.Instruction, joined)
	meta := map[string]any{
		"model": preferModel(subCtx, subagents.ModelHaiku),
		"tools": tools,
	}
	return subagents.Result{Output: output, Metadata: meta}, nil
}

func planHandler(ctx context.Context, subCtx subagents.Context, req subagents.Request) (subagents.Result, error) {
	if err := ctx.Err(); err != nil {
		return subagents.Result{}, err
	}
	steps := []string{
		"clarify objective and constraints",
		"split work into three executable tasks",
		"route execution to general-purpose agent after approval",
	}
	output := fmt.Sprintf("plan agent (%s) drafted steps for: %s\n- %s", preferModel(subCtx, subagents.ModelSonnet), req.Instruction, strings.Join(steps, "\n- "))
	return subagents.Result{
		Output: output,
		Metadata: map[string]any{
			"steps": len(steps),
			"tools": subCtx.ToolList(),
		},
	}, nil
}

func deployGuardHandler(ctx context.Context, subCtx subagents.Context, req subagents.Request) (subagents.Result, error) {
	if err := ctx.Err(); err != nil {
		return subagents.Result{}, err
	}
	output := fmt.Sprintf("deploy_guard stopped production rollout; forwarding context to planner. tools=%v", subCtx.ToolList())
	return subagents.Result{
		Output: output,
		Metadata: map[string]any{
			"blocked": true,
			"model":   preferModel(subCtx, subagents.ModelHaiku),
			"tools":   subCtx.ToolList(),
		},
	}, nil
}

func printBuiltinCatalog() {
	fmt.Println("== Builtin definitions ==")
	for _, def := range subagents.BuiltinDefinitions() {
		fmt.Printf("- %s (default model: %s) -> %s\n", def.Name, def.DefaultModel, def.Description)
	}
}

func printRegistered(mgr *subagents.Manager) {
	fmt.Println("\n== Registered subagents ==")
	for _, def := range mgr.List() {
		matcherMode := "auto"
		if len(def.Matchers) == 0 {
			matcherMode = "fallback"
		}
		fmt.Printf("- %s (priority %d, mutex=%s, %s) tools=%v model=%s\n", def.Name, def.Priority, def.MutexKey, matcherMode, def.BaseContext.ToolWhitelist, def.BaseContext.Model)
	}
}

func preferModel(subCtx subagents.Context, fallback string) string {
	model := strings.TrimSpace(subCtx.Model)
	if model != "" {
		return model
	}
	return fallback
}
