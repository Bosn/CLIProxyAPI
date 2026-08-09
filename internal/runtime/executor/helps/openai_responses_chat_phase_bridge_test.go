package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestApplyOpenAIResponsesChatPhaseBridge(t *testing.T) {
	t.Parallel()
	source := []byte(`{
		"input":[
			{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking."}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]}
		]
	}`)
	translated := []byte(`{
		"model":"qwen3.8-max",
		"messages":[
			{"role":"system","content":"Base instructions."},
			{"role":"assistant","content":[{"type":"text","text":"Checking."}]},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"assistant","content":[{"type":"text","text":"Done."}]},
			{"role":"user","content":[{"type":"text","text":"Continue."}]}
		]
	}`)

	got := ApplyOpenAIResponsesChatPhaseBridge(source, translated)
	if !strings.Contains(gjson.GetBytes(got, "messages.0.content").String(), openAIResponsesChatPhaseBridgeInstruction) {
		t.Fatalf("system instruction is missing phase bridge contract: %s", got)
	}
	if text := gjson.GetBytes(got, "messages.1.content.0.text").String(); text != "Checking." {
		t.Fatalf("commentary history changed: %q", text)
	}
	if text := gjson.GetBytes(got, "messages.4.content.0.text").String(); text != "Done." {
		t.Fatalf("final history changed: %q", text)
	}
	if gjson.GetBytes(got, "messages.2.tool_calls.0.function.name").String() != "lookup" {
		t.Fatalf("tool message changed: %s", got)
	}
}

func TestApplyOpenAIResponsesChatPhaseBridgeAddsSystemMessage(t *testing.T) {
	t.Parallel()
	got := ApplyOpenAIResponsesChatPhaseBridge(
		[]byte(`{"input":"hello"}`),
		[]byte(`{"model":"qwen3.8-max","messages":[{"role":"user","content":"hello"}]}`),
	)
	if gjson.GetBytes(got, "messages.0.role").String() != "system" {
		t.Fatalf("first message is not system: %s", got)
	}
	if !strings.Contains(gjson.GetBytes(got, "messages.0.content").String(), openAIResponsesChatPhaseBridgeInstruction) {
		t.Fatalf("phase bridge instruction is missing: %s", got)
	}
	if gjson.GetBytes(got, "messages.1.content").String() != "hello" {
		t.Fatalf("user message changed: %s", got)
	}
}

func TestApplyOpenAIResponsesChatPhaseBridgeKeepsUnphasedHistoryAligned(t *testing.T) {
	t.Parallel()
	source := []byte(`{"input":[
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Legacy."}]},
		{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Final."}]}
	]}`)
	translated := []byte(`{"messages":[
		{"role":"system","content":"base"},
		{"role":"assistant","content":[{"type":"text","text":"Legacy."}]},
		{"role":"assistant","content":[{"type":"text","text":"Final."}]}
	]}`)

	got := ApplyOpenAIResponsesChatPhaseBridge(source, translated)
	if text := gjson.GetBytes(got, "messages.1.content.0.text").String(); text != "Legacy." {
		t.Fatalf("legacy message was relabeled: %q", text)
	}
	if text := gjson.GetBytes(got, "messages.2.content.0.text").String(); text != "Final." {
		t.Fatalf("final history changed: %q", text)
	}
}

func TestApplyOpenAIResponsesChatPhaseBridgeNormalizesThinkingToolChoice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		sourceChoice     string
		translatedChoice string
		wantInstruction  string
	}{
		{
			name:             "named function",
			sourceChoice:     `{"type":"function","name":"lookup"}`,
			translatedChoice: `{"type":"function","function":{"name":"lookup"}}`,
			wantInstruction:  `requires a call to function "lookup"`,
		},
		{
			name:             "required",
			sourceChoice:     `"required"`,
			translatedChoice: `"required"`,
			wantInstruction:  "requires at least one tool call",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := []byte(`{"tool_choice":` + test.sourceChoice + `}`)
			translated := []byte(`{"tool_choice":` + test.translatedChoice + `,"messages":[{"role":"system","content":"base"}]}`)
			got := ApplyOpenAIResponsesChatPhaseBridge(source, translated)
			if choice := gjson.GetBytes(got, "tool_choice").String(); choice != "auto" {
				t.Fatalf("tool_choice = %q, want auto; body=%s", choice, got)
			}
			if instruction := gjson.GetBytes(got, "messages.0.content").String(); !strings.Contains(instruction, test.wantInstruction) {
				t.Fatalf("instruction %q does not contain %q", instruction, test.wantInstruction)
			}
		})
	}
}
