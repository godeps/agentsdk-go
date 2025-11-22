package api

import (
	"strings"

	"github.com/cexll/agentsdk-go/pkg/config"
	coreevents "github.com/cexll/agentsdk-go/pkg/core/events"
	corehooks "github.com/cexll/agentsdk-go/pkg/core/hooks"
)

func newHookExecutor(opts Options, recorder HookRecorder, settings *config.Settings) *corehooks.Executor {
	exec := corehooks.NewExecutor(corehooks.WithMiddleware(opts.HookMiddleware...), corehooks.WithTimeout(opts.HookTimeout))
	if len(opts.TypedHooks) > 0 {
		exec.Register(opts.TypedHooks...)
	}
	if !hooksDisabled(settings) {
		hooks := buildSettingsHooks(settings)
		if len(hooks) > 0 {
			exec.Register(hooks...)
		}
	}
	_ = recorder
	return exec
}

func hooksDisabled(settings *config.Settings) bool {
	return settings != nil && settings.DisableAllHooks != nil && *settings.DisableAllHooks
}

// buildSettingsHooks converts settings.Hooks config to ShellHook structs.
func buildSettingsHooks(settings *config.Settings) []corehooks.ShellHook {
	if settings == nil || settings.Hooks == nil {
		return nil
	}

	var hooks []corehooks.ShellHook
	env := map[string]string{}
	for k, v := range settings.Env {
		env[k] = v
	}

	// Build PreToolUse hooks
	for toolName, cmd := range settings.Hooks.PreToolUse {
		if cmd == "" {
			continue
		}
		selectorPattern := normalizeToolSelectorPattern(toolName)
		sel, err := corehooks.NewSelector(selectorPattern, "")
		if err != nil {
			// skip invalid selector patterns rather than failing runtime startup
			continue
		}
		hooks = append(hooks, corehooks.ShellHook{
			Event:    coreevents.PreToolUse,
			Command:  cmd,
			Selector: sel,
			Env:      env,
			Name:     "settings:pre:" + toolName,
		})
	}

	// Build PostToolUse hooks
	for toolName, cmd := range settings.Hooks.PostToolUse {
		if cmd == "" {
			continue
		}
		selectorPattern := normalizeToolSelectorPattern(toolName)
		sel, err := corehooks.NewSelector(selectorPattern, "")
		if err != nil {
			// skip invalid selector patterns rather than failing runtime startup
			continue
		}
		hooks = append(hooks, corehooks.ShellHook{
			Event:    coreevents.PostToolUse,
			Command:  cmd,
			Selector: sel,
			Env:      env,
			Name:     "settings:post:" + toolName,
		})
	}

	return hooks
}

// normalizeToolSelectorPattern maps wildcard "*" to the selector wildcard (empty pattern).
func normalizeToolSelectorPattern(pattern string) string {
	if strings.TrimSpace(pattern) == "*" {
		return ""
	}
	return pattern
}
