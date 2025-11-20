package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/cexll/agentsdk-go/pkg/runtime/commands"
)

func main() {
	ctx := context.Background()
	script := demoScript()

	invocations, err := commands.Parse(script)
	if err != nil {
		log.Fatalf("parse slash commands: %v", err)
	}

	fmt.Println("===== Parsed Invocations =====")
	dumpInvocations(invocations)

	fmt.Println("\n===== Execution =====")
	exec := buildExecutor()
	results, err := exec.Execute(ctx, invocations)
	if err != nil {
		log.Printf("execution stopped after error: %v", err)
	}
	for _, res := range results {
		fmt.Printf("/%s -> %v", res.Command, res.Output)
		if len(res.Metadata) > 0 {
			fmt.Printf(" (metadata: %v)", res.Metadata)
		}
		if res.Error != "" {
			fmt.Printf(" [error: %s]", res.Error)
		}
		fmt.Println()
	}
}

func demoScript() string {
	return strings.TrimSpace(`
/deploy staging --version 2025.11.20 --region=us-east-1 --force
/query "latency p95" --since "2025-11-20 08:00" --limit=3
/note add "release checklist" "/tmp/release plan.md" --tag "ops crew" --private
/backup run --path=/var/log/app --dest "./tmp/log backup" --compress
irrelevant text is ignored because it does not start with a slash
`)
}

func dumpInvocations(invocations []commands.Invocation) {
	for _, inv := range invocations {
		fmt.Printf("line %d: %s\n", inv.Position, inv.Raw)
		fmt.Printf("  name: %s\n", inv.Name)
		fmt.Printf("  args: %v\n", inv.Args)
		if len(inv.Flags) == 0 {
			fmt.Println("  flags: <none>")
			continue
		}
		keys := make([]string, 0, len(inv.Flags))
		for key := range inv.Flags {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("  flag --%s=%s\n", key, inv.Flags[key])
		}
	}
}

func buildExecutor() *commands.Executor {
	exec := commands.NewExecutor()
	must(exec.Register(commands.Definition{Name: "deploy", Description: "deploy artifact"}, commands.HandlerFunc(handleDeploy)))
	must(exec.Register(commands.Definition{Name: "query", Description: "run read-only queries"}, commands.HandlerFunc(handleQuery)))
	must(exec.Register(commands.Definition{Name: "note", Description: "store small notes"}, commands.HandlerFunc(handleNote)))
	must(exec.Register(commands.Definition{Name: "backup", Description: "ship logs somewhere"}, commands.HandlerFunc(handleBackup)))
	return exec
}

func handleDeploy(_ context.Context, inv commands.Invocation) (commands.Result, error) {
	if len(inv.Args) == 0 {
		return commands.Result{}, errors.New("deploy: target environment is required")
	}
	env := inv.Args[0]
	version := flagValue(inv, "version", "latest")
	region := flagValue(inv, "region", "us-east-1")
	force := flagBool(inv, "force")

	output := fmt.Sprintf("deploying to %s with version %s (region %s, force=%t)", env, version, region, force)
	return commands.Result{
		Output: output,
		Metadata: map[string]any{
			"args":  inv.Args,
			"force": force,
		},
	}, nil
}

func handleQuery(_ context.Context, inv commands.Invocation) (commands.Result, error) {
	if len(inv.Args) == 0 {
		return commands.Result{}, errors.New("query: search term is required")
	}
	term := inv.Args[0]
	since := flagValue(inv, "since", "(none)")
	limit := flagValue(inv, "limit", "unbounded")

	output := fmt.Sprintf("query term=%q since=%s limit=%s", term, since, limit)
	return commands.Result{Output: output}, nil
}

func handleNote(_ context.Context, inv commands.Invocation) (commands.Result, error) {
	if len(inv.Args) < 2 {
		return commands.Result{}, errors.New("note: need action and body text")
	}
	action, body := inv.Args[0], inv.Args[1]
	tag := flagValue(inv, "tag", "")
	private := flagBool(inv, "private")

	meta := map[string]any{"action": action, "private": private}
	if tag != "" {
		meta["tag"] = tag
	}
	return commands.Result{Output: fmt.Sprintf("note %s: %s", action, body), Metadata: meta}, nil
}

func handleBackup(_ context.Context, inv commands.Invocation) (commands.Result, error) {
	path := flagValue(inv, "path", "")
	dest := flagValue(inv, "dest", "")
	compress := flagBool(inv, "compress")
	if path == "" || dest == "" {
		return commands.Result{}, errors.New("backup: path and dest are required")
	}
	summary := fmt.Sprintf("backup from %s to %s (compress=%t)", path, dest, compress)
	return commands.Result{Output: summary}, nil
}

func flagValue(inv commands.Invocation, name, fallback string) string {
	if v, ok := inv.Flag(name); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func flagBool(inv commands.Invocation, name string) bool {
	v, ok := inv.Flag(name)
	if !ok {
		return false
	}
	if v == "" {
		return true
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
