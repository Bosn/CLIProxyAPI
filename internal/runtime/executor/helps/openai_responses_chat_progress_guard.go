package helps

import (
	"bytes"
	"strconv"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIResponsesChatProgressRetryInstruction = `The previous generation ended with intermediate progress text but did not call a tool. Continue the pending work now by calling the next appropriate tool in this response. Do not repeat or summarize the progress preamble.`

// OpenAIResponsesChatProgressGuard observes an upstream Chat Completions stream
// before any bytes are emitted downstream. It identifies the narrow failure
// mode where a model promises more work but stops without a tool call.
type OpenAIResponsesChatProgressGuard struct {
	text         strings.Builder
	sawToolCall  bool
	finishReason string
}

// Observe records one OpenAI-compatible SSE data line.
func (g *OpenAIResponsesChatProgressGuard) Observe(line []byte) {
	if g == nil {
		return
	}
	payload := bytes.TrimSpace(line)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[5:])
	}
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !gjson.ValidBytes(payload) {
		return
	}
	choices := gjson.GetBytes(payload, "choices")
	if !choices.IsArray() {
		return
	}
	choices.ForEach(func(_, choice gjson.Result) bool {
		delta := choice.Get("delta")
		if content := delta.Get("content"); content.Type == gjson.String {
			g.text.WriteString(content.String())
		}
		if calls := delta.Get("tool_calls"); calls.IsArray() && len(calls.Array()) > 0 {
			g.sawToolCall = true
		}
		if message := choice.Get("message"); message.Exists() {
			if content := message.Get("content"); content.Type == gjson.String {
				g.text.WriteString(content.String())
			}
			if calls := message.Get("tool_calls"); calls.IsArray() && len(calls.Array()) > 0 {
				g.sawToolCall = true
			}
		}
		if finishReason := strings.TrimSpace(choice.Get("finish_reason").String()); finishReason != "" {
			g.finishReason = finishReason
		}
		return true
	})
}

// SawToolCall reports whether the observed turn contains any tool call.
func (g *OpenAIResponsesChatProgressGuard) SawToolCall() bool {
	return g != nil && g.sawToolCall
}

// ShouldRetry reports whether the complete turn is a progress-only stop that
// can be retried safely because nothing has been emitted downstream.
func (g *OpenAIResponsesChatProgressGuard) ShouldRetry() bool {
	return g != nil && !g.sawToolCall && g.finishReason == "stop" && translatorcommon.OpenAIResponsesLooksLikeProgressOnly(g.text.String())
}

// ApplyOpenAIResponsesChatProgressRetryInstruction strengthens one retry after
// a progress-only response. It does not force a synthetic tool call.
func ApplyOpenAIResponsesChatProgressRetryInstruction(translated []byte) []byte {
	if !gjson.ValidBytes(translated) {
		return translated
	}
	messages := gjson.GetBytes(translated, "messages")
	if !messages.IsArray() {
		return translated
	}
	for index, message := range messages.Array() {
		if message.Get("role").String() != "system" || message.Get("content").Type != gjson.String {
			continue
		}
		content := strings.TrimSpace(message.Get("content").String())
		if strings.Contains(content, openAIResponsesChatProgressRetryInstruction) {
			return translated
		}
		if content != "" {
			content += "\n\n"
		}
		updated, errSet := sjson.SetBytes(translated, "messages."+strconv.Itoa(index)+".content", content+openAIResponsesChatProgressRetryInstruction)
		if errSet == nil {
			return updated
		}
		return translated
	}

	systemMessage := []byte(`{"role":"system","content":""}`)
	systemMessage, _ = sjson.SetBytes(systemMessage, "content", openAIResponsesChatProgressRetryInstruction)
	wrapper := []byte(`{"messages":[]}`)
	wrapper, _ = sjson.SetRawBytes(wrapper, "messages.-1", systemMessage)
	for _, message := range messages.Array() {
		wrapper, _ = sjson.SetRawBytes(wrapper, "messages.-1", []byte(message.Raw))
	}
	updated, errSet := sjson.SetRawBytes(translated, "messages", []byte(gjson.GetBytes(wrapper, "messages").Raw))
	if errSet != nil {
		return translated
	}
	return updated
}
