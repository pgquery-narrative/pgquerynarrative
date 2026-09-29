package llm

import (
	"context"
	"strings"
)

// messageBoundary separates system instructions from user/untrusted content when a
// prompt is flattened to a single string for providers that only accept one blob.
// GenerateWithUsage re-splits on this marker into multi-role chat messages.
const messageBoundary = "\n<<<PGQN_MESSAGE_BOUNDARY>>>\n"

// ChatMessage is one role-tagged message sent to a chat-completions-style provider.
type ChatMessage struct {
	Role    string // "system", "user", or "assistant"
	Content string
}

// MessagesClient is implemented by providers that accept structured multi-role messages.
type MessagesClient interface {
	GenerateMessages(ctx context.Context, messages []ChatMessage) (GenerationResult, error)
}

// FlattenMessages joins structured messages into a single prompt string for token
// estimation and for providers that only accept a flat prompt. The boundary marker
// lets GenerateWithUsage restore roles when the provider supports MessagesClient.
func FlattenMessages(messages []ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}
	if len(messages) == 1 {
		return messages[0].Content
	}
	var parts []string
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		parts = append(parts, role+"\n"+m.Content)
	}
	return strings.Join(parts, messageBoundary)
}

// PromptToChatMessages restores multi-role messages from a FlattenMessages prompt.
// Prompts without the boundary become a single user message.
func PromptToChatMessages(prompt string) []ChatMessage {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	if !strings.Contains(prompt, messageBoundary) {
		return []ChatMessage{{Role: "user", Content: prompt}}
	}
	chunks := strings.Split(prompt, messageBoundary)
	out := make([]ChatMessage, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		role := "user"
		content := chunk
		if i := strings.IndexByte(chunk, '\n'); i > 0 {
			maybeRole := strings.ToLower(strings.TrimSpace(chunk[:i]))
			switch maybeRole {
			case "system", "user", "assistant":
				role = maybeRole
				content = strings.TrimSpace(chunk[i+1:])
			}
		}
		out = append(out, ChatMessage{Role: role, Content: content})
	}
	if len(out) == 0 {
		return []ChatMessage{{Role: "user", Content: prompt}}
	}
	return out
}

// chatMessagesPayload converts ChatMessage values to the OpenAI/Groq JSON shape.
func chatMessagesPayload(messages []ChatMessage) []map[string]string {
	out := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		out = append(out, map[string]string{"role": role, "content": m.Content})
	}
	return out
}
