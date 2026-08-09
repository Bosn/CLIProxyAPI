package common

import (
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIResponsesChatPhaseBridgeField = "_cliproxy_responses_chat_phase_bridge"

// MarkOpenAIResponsesChatPhaseBridge marks a local translation copy so the
// response converter can reconstruct assistant phases. The marked copy must
// not be sent upstream.
func MarkOpenAIResponsesChatPhaseBridge(request []byte) []byte {
	if !gjson.ValidBytes(request) {
		return request
	}
	marked, errSet := sjson.SetBytes(request, openAIResponsesChatPhaseBridgeField, true)
	if errSet != nil {
		return request
	}
	return marked
}

// OpenAIResponsesChatPhaseBridgeEnabled reports whether the translated Chat
// request opted into assistant phase reconstruction.
func OpenAIResponsesChatPhaseBridgeEnabled(request []byte) bool {
	if !gjson.ValidBytes(request) {
		return false
	}
	return gjson.GetBytes(request, openAIResponsesChatPhaseBridgeField).Bool()
}

// OpenAIResponsesAssistantPhase reconstructs the Responses phase for a Chat
// assistant message. Tool-bearing messages are always commentary. A text-only
// message is commentary only when its ending explicitly promises more work;
// otherwise it is a final answer.
func OpenAIResponsesAssistantPhase(text string, hasToolCall bool) string {
	if hasToolCall {
		return "commentary"
	}
	if OpenAIResponsesLooksLikeProgressOnly(text) {
		return "commentary"
	}
	return "final_answer"
}

// OpenAIResponsesLooksLikeProgressOnly reports whether assistant text ends in
// an explicit promise to perform more work instead of a completed answer.
func OpenAIResponsesLooksLikeProgressOnly(text string) bool {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return false
	}
	const maxTailRunes = 360
	if utf8.RuneCountInString(text) > maxTailRunes {
		runes := []rune(text)
		text = string(runes[len(runes)-maxTailRunes:])
	}
	futureSignals := []string{
		"let me ",
		"i'll ",
		"i will ",
		"now i'll ",
		"now i will ",
		"next i'll ",
		"next i will ",
		"接下来",
		"下一步",
		"让我",
		"我再",
		"我会",
		"我将",
		"现在我来",
		"现在我去",
	}
	actionSignals := []string{
		"check",
		"inspect",
		"verify",
		"investigate",
		"trace",
		"read",
		"look at",
		"run ",
		"查询",
		"检查",
		"核对",
		"验证",
		"排查",
		"查看",
		"读取",
		"运行",
		"继续",
	}
	return containsAny(text, futureSignals) && containsAny(text, actionSignals)
}

func containsAny(text string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}
