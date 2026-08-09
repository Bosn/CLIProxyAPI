package common

import "testing"

func TestOpenAIResponsesChatPhaseBridgeEnabled(t *testing.T) {
	t.Parallel()
	request := []byte(`{"messages":[{"role":"system","content":"ordinary"}]}`)
	if OpenAIResponsesChatPhaseBridgeEnabled(request) {
		t.Fatal("unmarked request unexpectedly enabled the phase bridge")
	}
	marked := MarkOpenAIResponsesChatPhaseBridge(request)
	if !OpenAIResponsesChatPhaseBridgeEnabled(marked) {
		t.Fatalf("marked request did not enable the phase bridge: %s", marked)
	}
	if OpenAIResponsesChatPhaseBridgeEnabled([]byte(`{`)) {
		t.Fatal("invalid request unexpectedly enabled the phase bridge")
	}
}

func TestOpenAIResponsesAssistantPhase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		text        string
		hasToolCall bool
		want        string
	}{
		{name: "tool call", text: "Checking now.", hasToolCall: true, want: "commentary"},
		{name: "first incident", text: "I've traced the request flow. Now I'll check what Qwen accepts upstream.", want: "commentary"},
		{name: "second incident", text: "No listener on 417x. Let me check the publisher state and what last got published.", want: "commentary"},
		{name: "Chinese progress", text: "源码已经确认。接下来我会检查发布状态。", want: "commentary"},
		{name: "final", text: "Root cause: the compiled Viewer was not released. No further work is pending.", want: "final_answer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := OpenAIResponsesAssistantPhase(test.text, test.hasToolCall); got != test.want {
				t.Fatalf("phase = %q, want %q", got, test.want)
			}
		})
	}
}
