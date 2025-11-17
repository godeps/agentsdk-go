package middleware

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/cexll/agentsdk-go/pkg/model"
)

const summaryBypassKey = "middleware.summarization.skip"

// SummarizationMiddleware trims long histories by summarizing older turns.
type SummarizationMiddleware struct {
	*BaseMiddleware
	maxTokens  int
	keepRecent int
	prompt     string
}

// NewSummarizationMiddleware constructs a summarization middleware with sensible defaults.
func NewSummarizationMiddleware(maxTokens, keepRecent int) *SummarizationMiddleware {
	if keepRecent <= 0 {
		keepRecent = 6
	}
	if maxTokens <= 0 {
		maxTokens = 120000
	}
	return &SummarizationMiddleware{
		BaseMiddleware: NewBaseMiddleware("summarization", 50),
		maxTokens:      maxTokens,
		keepRecent:     keepRecent,
		prompt:         "请将以下对话历史总结为结构化要点，保留事实、意图和未完成事项：\n\n",
	}
}

// ExecuteModelCall inspects the request and condenses excessive history before continuing.
func (m *SummarizationMiddleware) ExecuteModelCall(ctx context.Context, req *ModelRequest, next ModelCallFunc) (*ModelResponse, error) {
	if next == nil {
		return nil, ErrMissingNext
	}
	if req == nil {
		return next(ctx, req)
	}
	if req.Metadata != nil {
		if skip, ok := req.Metadata[summaryBypassKey].(bool); ok && skip {
			return next(ctx, req)
		}
	}
	if !m.shouldSummarize(req.Messages) {
		return next(ctx, req)
	}
	summarized, err := m.buildSummary(ctx, req, next)
	if err != nil {
		log.Printf("middleware: summarization failed (%v), fallback to truncation", err)
		req.Messages = m.truncateMessages(req.Messages)
	} else if len(summarized) > 0 {
		req.Messages = summarized
		if req.Metadata == nil {
			req.Metadata = map[string]any{}
		}
		req.Metadata["summarization_applied"] = true
	}
	return next(ctx, req)
}

func (m *SummarizationMiddleware) shouldSummarize(messages []model.Message) bool {
	if m == nil || m.maxTokens <= 0 {
		return false
	}
	if len(messages) == 0 {
		return false
	}
	if len(messages) <= m.keepRecent+1 {
		return false
	}
	return m.estimateTokens(messages) > m.maxTokens
}

func (m *SummarizationMiddleware) estimateTokens(messages []model.Message) int {
	total := 0
	for _, msg := range messages {
		total += utf8.RuneCountInString(msg.Content)
		for _, call := range msg.ToolCalls {
			total += len(call.Name)
			total += len(call.ID)
			for k, v := range call.Arguments {
				total += len(k) + utf8.RuneCountInString(fmt.Sprint(v))
			}
		}
	}
	// 粗略估算：4 个字符约等于 1 token。
	return total / 4
}

func (m *SummarizationMiddleware) buildSummary(ctx context.Context, req *ModelRequest, next ModelCallFunc) ([]model.Message, error) {
	if len(req.Messages) == 0 {
		return nil, nil
	}
	head := m.leadingSystemMessages(req.Messages)
	keep := m.keepRecent
	if keep >= len(req.Messages) {
		return req.Messages, nil
	}
	tailStart := len(req.Messages) - keep
	if tailStart <= head {
		return req.Messages, nil
	}
	old := cloneMessages(req.Messages[head:tailStart])
	if len(old) == 0 {
		return req.Messages, nil
	}

	builder := strings.Builder{}
	builder.WriteString(m.prompt)
	for _, msg := range old {
		builder.WriteString(fmt.Sprintf("[%s] %s\n", msg.Role, strings.TrimSpace(msg.Content)))
	}

	summaryReq := &ModelRequest{
		Messages:  []model.Message{{Role: "user", Content: builder.String()}},
		SessionID: req.SessionID,
		Metadata:  map[string]any{summaryBypassKey: true},
	}
	resp, err := next(ctx, summaryReq)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("summarization: nil response")
	}
	summary := strings.TrimSpace(resp.Message.Content)
	if summary == "" {
		summary = m.naiveSummary(old)
	}
	var condensed []model.Message
	condensed = append(condensed, cloneMessages(req.Messages[:head])...)
	condensed = append(condensed, model.Message{
		Role:    "system",
		Content: "历史摘要：\n" + summary,
	})
	condensed = append(condensed, cloneMessages(req.Messages[tailStart:])...)
	return condensed, nil
}

func (m *SummarizationMiddleware) leadingSystemMessages(messages []model.Message) int {
	count := 0
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "system" {
			break
		}
		count++
	}
	return count
}

func (m *SummarizationMiddleware) truncateMessages(messages []model.Message) []model.Message {
	head := m.leadingSystemMessages(messages)
	if len(messages) <= head+m.keepRecent {
		return messages
	}
	tailStart := len(messages) - m.keepRecent
	if tailStart <= head {
		return messages
	}
	var trimmed []model.Message
	trimmed = append(trimmed, cloneMessages(messages[:head])...)
	trimmed = append(trimmed, model.Message{
		Role:    "system",
		Content: "历史摘要：对话过长，已裁剪早期轮次以控制上下文。",
	})
	trimmed = append(trimmed, cloneMessages(messages[tailStart:])...)
	return trimmed
}

func (m *SummarizationMiddleware) naiveSummary(messages []model.Message) string {
	const maxSentences = 6
	summary := make([]string, 0, maxSentences)
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		summary = append(summary, fmt.Sprintf("%s: %s", msg.Role, content))
		if len(summary) >= maxSentences {
			break
		}
	}
	return strings.Join(summary, "\n")
}

func cloneMessages(messages []model.Message) []model.Message {
	out := make([]model.Message, len(messages))
	for i, msg := range messages {
		msgCopy := msg
		if len(msg.ToolCalls) > 0 {
			toolCalls := make([]model.ToolCall, len(msg.ToolCalls))
			copy(toolCalls, msg.ToolCalls)
			msgCopy.ToolCalls = toolCalls
		}
		out[i] = msgCopy
	}
	return out
}
