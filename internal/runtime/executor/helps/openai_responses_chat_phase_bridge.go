package helps

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIResponsesChatPhaseBridgeInstruction = `This request is translated from the OpenAI Responses API to Chat Completions.
Treat assistant phases as a strict execution contract:
- If work remains, call the next required tool in this same response. Any text before or alongside a tool call is intermediate commentary.
- Never end a response with a text-only preamble such as "let me check", "I'll inspect", or a promise to do more work.
- Return a text-only response only when the user's task is genuinely complete; that text is the final answer.`

// ApplyOpenAIResponsesChatPhaseBridge adds the phase contract to a translated
// Chat request without placing internal phase metadata in model-visible text.
func ApplyOpenAIResponsesChatPhaseBridge(source, translated []byte) []byte {
	if !gjson.ValidBytes(source) || !gjson.ValidBytes(translated) {
		return translated
	}
	out, toolChoiceInstruction := normalizeOpenAIResponsesChatThinkingToolChoice(source, translated)
	return addOpenAIResponsesChatPhaseInstruction(out, toolChoiceInstruction)
}

func normalizeOpenAIResponsesChatThinkingToolChoice(source, translated []byte) ([]byte, string) {
	choice := gjson.GetBytes(source, "tool_choice")
	if choice.Type == gjson.String && choice.String() == "required" {
		updated, errSet := sjson.SetBytes(translated, "tool_choice", "auto")
		if errSet != nil {
			return translated, ""
		}
		return updated, "This turn requires at least one tool call; use the appropriate available function."
	}
	if !choice.IsObject() || choice.Get("type").String() != "function" {
		return translated, ""
	}
	name := strings.TrimSpace(choice.Get("name").String())
	if name == "" {
		return translated, ""
	}
	updated, errSet := sjson.SetBytes(translated, "tool_choice", "auto")
	if errSet != nil {
		return translated, ""
	}
	return updated, "This turn requires a call to function " + strconv.Quote(name) + "; do not substitute another function."
}

func addOpenAIResponsesChatPhaseInstruction(translated []byte, toolChoiceInstruction string) []byte {
	messages := gjson.GetBytes(translated, "messages")
	if !messages.IsArray() {
		return translated
	}
	instruction := openAIResponsesChatPhaseBridgeInstruction
	if toolChoiceInstruction != "" {
		instruction += "\n- " + toolChoiceInstruction
	}
	for index, message := range messages.Array() {
		if message.Get("role").String() != "system" {
			continue
		}
		content := message.Get("content")
		if content.Type != gjson.String {
			continue
		}
		text := strings.TrimSpace(content.String())
		if strings.Contains(text, openAIResponsesChatPhaseBridgeInstruction) {
			return translated
		}
		if text != "" {
			text += "\n\n"
		}
		updated, errSet := sjson.SetBytes(translated, "messages."+strconv.Itoa(index)+".content", text+instruction)
		if errSet == nil {
			return updated
		}
		return translated
	}

	wrapped := []byte(`{"messages":[]}`)
	systemMessage := []byte(`{"role":"system","content":""}`)
	systemMessage, _ = sjson.SetBytes(systemMessage, "content", instruction)
	wrapped, _ = sjson.SetRawBytes(wrapped, "messages.-1", systemMessage)
	for _, message := range messages.Array() {
		wrapped, _ = sjson.SetRawBytes(wrapped, "messages.-1", []byte(message.Raw))
	}
	updated, errSet := sjson.SetRawBytes(translated, "messages", []byte(gjson.GetBytes(wrapped, "messages").Raw))
	if errSet != nil {
		return translated
	}
	return updated
}
