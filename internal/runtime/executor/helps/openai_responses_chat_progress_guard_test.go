package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesChatProgressGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		lines []string
		want  bool
	}{
		{
			name: "progress-only stop",
			lines: []string{
				`data: {"choices":[{"index":0,"delta":{"content":"Source checked. Next I'll inspect "},"finish_reason":null}]}`,
				`data: {"choices":[{"index":0,"delta":{"content":"the live configuration."},"finish_reason":"stop"}]}`,
			},
			want: true,
		},
		{
			name:  "final answer",
			lines: []string{`data: {"choices":[{"index":0,"delta":{"content":"The configuration is correct and no work remains."},"finish_reason":"stop"}]}`},
		},
		{
			name: "tool call",
			lines: []string{
				`data: {"choices":[{"index":0,"delta":{"content":"Next I'll inspect the config."},"finish_reason":null}]}`,
				`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"inspect","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var guard OpenAIResponsesChatProgressGuard
			for _, line := range test.lines {
				guard.Observe([]byte(line))
			}
			if got := guard.ShouldRetry(); got != test.want {
				t.Fatalf("ShouldRetry() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestApplyOpenAIResponsesChatProgressRetryInstruction(t *testing.T) {
	t.Parallel()
	request := []byte(`{"messages":[{"role":"system","content":"base"},{"role":"user","content":"continue"}]}`)
	got := ApplyOpenAIResponsesChatProgressRetryInstruction(request)
	content := gjson.GetBytes(got, "messages.0.content").String()
	if !strings.Contains(content, "previous generation ended with intermediate progress text") {
		t.Fatalf("retry instruction is missing: %s", got)
	}
	if user := gjson.GetBytes(got, "messages.1.content").String(); user != "continue" {
		t.Fatalf("user message changed: %q", user)
	}
	again := ApplyOpenAIResponsesChatProgressRetryInstruction(got)
	if strings.Count(gjson.GetBytes(again, "messages.0.content").String(), "previous generation ended with intermediate progress text") != 1 {
		t.Fatalf("retry instruction was duplicated: %s", again)
	}
}
