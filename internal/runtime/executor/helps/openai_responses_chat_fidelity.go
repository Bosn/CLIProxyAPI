package helps

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ApplyOpenAIResponsesChatFidelity restores Responses request fields that the
// generic Responses-to-Chat translation cannot represent faithfully.
func ApplyOpenAIResponsesChatFidelity(source, translated []byte) []byte {
	if !gjson.ValidBytes(source) || !gjson.ValidBytes(translated) {
		return translated
	}

	out := applyOpenAIResponsesNamedToolChoice(source, translated)
	return applyOpenAIResponsesToolStrict(source, out)
}

func applyOpenAIResponsesNamedToolChoice(source, translated []byte) []byte {
	choice := gjson.GetBytes(source, "tool_choice")
	if !choice.IsObject() || choice.Get("type").String() != "function" || choice.Get("function").Exists() {
		return translated
	}
	name := choice.Get("name")
	if name.Type != gjson.String || strings.TrimSpace(name.String()) == "" {
		return translated
	}

	translatedChoice := gjson.GetBytes(translated, "tool_choice")
	if translatedChoice.Exists() {
		if !translatedChoice.IsObject() || translatedChoice.Get("function").Exists() {
			return translated
		}
		if translatedType := translatedChoice.Get("type"); translatedType.Exists() && translatedType.String() != "function" {
			return translated
		}
	}

	updated, errSetType := sjson.SetBytes(translated, "tool_choice.type", "function")
	if errSetType != nil {
		return translated
	}
	updated, errSetName := sjson.SetBytes(updated, "tool_choice.function.name", name.String())
	if errSetName != nil {
		return translated
	}
	updated, errDeleteName := sjson.DeleteBytes(updated, "tool_choice.name")
	if errDeleteName != nil {
		return translated
	}
	return updated
}

func applyOpenAIResponsesToolStrict(source, translated []byte) []byte {
	sourceTools := gjson.GetBytes(source, "tools")
	translatedTools := gjson.GetBytes(translated, "tools")
	if !sourceTools.IsArray() || !translatedTools.IsArray() {
		return translated
	}

	out := translated
	translatedItems := translatedTools.Array()
	used := make([]bool, len(translatedItems))
	for sourceIndex, sourceTool := range sourceTools.Array() {
		if !sourceTool.IsObject() || sourceTool.Get("type").String() != "function" {
			continue
		}
		strict := sourceTool.Get("strict")
		if strict.Type != gjson.True && strict.Type != gjson.False {
			continue
		}

		name := sourceTool.Get("name").String()
		targetIndex := -1
		if sourceIndex < len(translatedItems) && openAIChatFunctionToolMatches(translatedItems[sourceIndex], name) {
			targetIndex = sourceIndex
		}
		if targetIndex == -1 {
			for translatedIndex, translatedTool := range translatedItems {
				if used[translatedIndex] || !openAIChatFunctionToolMatches(translatedTool, name) {
					continue
				}
				targetIndex = translatedIndex
				break
			}
		}
		if targetIndex == -1 {
			continue
		}

		updated, errSet := sjson.SetRawBytes(out, fmt.Sprintf("tools.%d.function.strict", targetIndex), []byte(strict.Raw))
		if errSet != nil {
			continue
		}
		out = updated
		used[targetIndex] = true
	}
	return out
}

func openAIChatFunctionToolMatches(tool gjson.Result, sourceName string) bool {
	if !tool.IsObject() || tool.Get("type").String() != "function" || !tool.Get("function").IsObject() {
		return false
	}
	return sourceName == "" || tool.Get("function.name").String() == sourceName
}
