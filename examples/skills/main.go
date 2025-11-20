package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cexll/agentsdk-go/pkg/runtime/skills"
)

type runConfig struct {
	prompt   string
	env      string
	severity string
	channels []string
	traits   []string
	timeout  time.Duration
	manual   string
}

func main() {
	cfg := parseConfig()
	ac := buildActivationContext(cfg)
	reg := buildRegistry()

	fmt.Println("== Activation context ==")
	fmt.Printf("prompt: %s\n", ac.Prompt)
	fmt.Printf("tags: %v\n", ac.Tags)
	fmt.Printf("channels: %v\n", ac.Channels)
	fmt.Printf("traits: %v\n", ac.Traits)

	fmt.Println("\n== Registered skills ==")
	for _, def := range reg.List() {
		mode := "auto"
		if def.DisableAutoActivation {
			mode = "manual"
		}
		fmt.Printf("- %s (priority %d, %s", def.Name, def.Priority, mode)
		if def.MutexKey != "" {
			fmt.Printf(", mutex=%s", def.MutexKey)
		}
		fmt.Printf(")\n  %s\n", def.Description)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	fmt.Println("\n== Auto activation ==")
	activations := reg.Match(ac)
	if len(activations) == 0 {
		fmt.Println("no skill matched; adjust flags to trigger different paths")
	} else {
		for _, activation := range activations {
			res, err := activation.Skill.Execute(ctx, ac)
			if err != nil {
				log.Printf("skill %s failed: %v", activation.Skill.Definition().Name, err)
				continue
			}
			fmt.Printf("- %s (score %.2f, reason %s)\n  output: %v\n", res.Skill, activation.Score, activation.Reason, res.Output)
			if len(res.Metadata) > 0 {
				fmt.Printf("  metadata: %v\n", res.Metadata)
			}
		}
	}

	fmt.Println("\n== Manual execution ==")
	res, err := reg.Execute(ctx, cfg.manual, ac)
	if err != nil {
		log.Fatalf("manual skill %s failed: %v", cfg.manual, err)
	}
	fmt.Printf("- %s -> %v\n", res.Skill, res.Output)
	if len(res.Metadata) > 0 {
		fmt.Printf("  metadata: %v\n", res.Metadata)
	}
}

func parseConfig() runConfig {
	var (
		rawChannels string
		rawTraits   string
	)
	cfg := runConfig{}
	flag.StringVar(&cfg.prompt, "prompt", "分析生产日志发现异常 SSH 尝试", "demo prompt text")
	flag.StringVar(&cfg.env, "env", "prod", "tag: environment")
	flag.StringVar(&cfg.severity, "severity", "high", "tag: severity level")
	flag.StringVar(&rawChannels, "channels", "cli,slack", "comma-separated channels")
	flag.StringVar(&rawTraits, "traits", "sre,security", "comma-separated traits")
	flag.StringVar(&cfg.manual, "manual-skill", "add_note", "skill name for manual execution")
	flag.DurationVar(&cfg.timeout, "timeout", 1200*time.Millisecond, "per-skill timeout")
	flag.Parse()
	cfg.channels = splitList(rawChannels)
	cfg.traits = splitList(rawTraits)
	return cfg
}

func buildActivationContext(cfg runConfig) skills.ActivationContext {
	return skills.ActivationContext{
		Prompt:   cfg.prompt,
		Channels: cfg.channels,
		Tags: map[string]string{
			"env":      cfg.env,
			"severity": cfg.severity,
		},
		Traits: cfg.traits,
		Metadata: map[string]any{
			"request_id": "skills-demo",
		},
	}
}

func buildRegistry() *skills.Registry {
	reg := skills.NewRegistry()

	mustRegister(reg, skills.Definition{
		Name:        "guardrail",
		Description: "阻断高危生产操作，只在严重等级高时触发。",
		Priority:    20,
		MutexKey:    "incident",
		Matchers: []skills.Matcher{
			skills.TagMatcher{Require: map[string]string{"env": "prod", "severity": "high"}},
			skills.KeywordMatcher{Any: []string{"incident", "breach", "告警", "事故"}},
		},
	}, skills.HandlerFunc(func(ctx context.Context, ac skills.ActivationContext) (skills.Result, error) {
		select {
		case <-ctx.Done():
			return skills.Result{}, ctx.Err()
		case <-time.After(60 * time.Millisecond):
		}
		return skills.Result{
			Output: fmt.Sprintf("已冻结高危指令，env=%s severity=%s", ac.Tags["env"], ac.Tags["severity"]),
			Metadata: map[string]any{
				"action":     "halt",
				"request_id": ac.Metadata["request_id"],
			},
		}, nil
	}))

	mustRegister(reg, skills.Definition{
		Name:        "log_summary",
		Description: "提炼 noisy 日志并输出一句话总结。",
		Priority:    10,
		Matchers: []skills.Matcher{
			skills.KeywordMatcher{Any: []string{"log", "日志", "error"}},
			channelMatcher("cli", 0.62),
		},
	}, skills.HandlerFunc(func(ctx context.Context, ac skills.ActivationContext) (skills.Result, error) {
		select {
		case <-ctx.Done():
			return skills.Result{}, ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
		return skills.Result{
			Output: fmt.Sprintf("日志概要：%s（渠道=%v）", ac.Prompt, ac.Channels),
			Metadata: map[string]any{
				"summary_tokens": 24,
			},
		}, nil
	}))

	mustRegister(reg, skills.Definition{
		Name:     "notify_chatops",
		Priority: 12,
		MutexKey: "incident",
		Description: "向 ChatOps 发送简短播报，" +
			"只有在通道包含 slack/teams 等协作渠道时自动触发。",
		Matchers: []skills.Matcher{
			channelMatcher("slack", 0.64),
			skills.KeywordMatcher{Any: []string{"alert", "incident", "播报"}},
		},
	}, skills.HandlerFunc(func(ctx context.Context, ac skills.ActivationContext) (skills.Result, error) {
		select {
		case <-ctx.Done():
			return skills.Result{}, ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
		return skills.Result{
			Output:   fmt.Sprintf("已推送 ChatOps 通知：%s", ac.Prompt),
			Metadata: map[string]any{"channel": strings.Join(ac.Channels, ",")},
		}, nil
	}))

	mustRegister(reg, skills.Definition{
		Name:                  "add_note",
		Description:           "手动添加备注，演示 DisableAutoActivation 用法。",
		DisableAutoActivation: true,
	}, skills.HandlerFunc(func(ctx context.Context, ac skills.ActivationContext) (skills.Result, error) {
		select {
		case <-ctx.Done():
			return skills.Result{}, ctx.Err()
		default:
		}
		return skills.Result{
			Output:   fmt.Sprintf("已记录备注：%s", ac.Prompt),
			Metadata: map[string]any{"manual": true},
		}, nil
	}))

	return reg
}

func channelMatcher(channel string, score float64) skills.Matcher {
	target := strings.ToLower(strings.TrimSpace(channel))
	return skills.MatcherFunc(func(ac skills.ActivationContext) skills.MatchResult {
		for _, ch := range ac.Channels {
			if strings.ToLower(strings.TrimSpace(ch)) == target {
				return skills.MatchResult{Matched: true, Score: score, Reason: "channel:" + target}
			}
		}
		return skills.MatchResult{}
	})
}

func splitList(input string) []string {
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token != "" {
			out = append(out, token)
		}
	}
	return out
}

func mustRegister(reg *skills.Registry, def skills.Definition, handler skills.Handler) {
	if err := reg.Register(def, handler); err != nil {
		log.Fatalf("register skill %s: %v", def.Name, err)
	}
}
