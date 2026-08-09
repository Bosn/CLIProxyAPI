package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestApplyOpenAIResponsesChatFidelity(t *testing.T) {
	source := []byte(`{
		"tool_choice":{"type":"function","name":"lookup"},
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"},"strict":false},
			{"type":"function","name":"other","parameters":{"type":"object"},"strict":true}
		]
	}`)
	translated := []byte(`{
		"tool_choice":{"type":"function","name":"lookup"},
		"tools":[
			{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"other","parameters":{"type":"object"}}}
		]
	}`)

	got := ApplyOpenAIResponsesChatFidelity(source, translated)
	if name := gjson.GetBytes(got, "tool_choice.function.name").String(); name != "lookup" {
		t.Fatalf("tool_choice.function.name = %q, want lookup; body=%s", name, got)
	}
	if gjson.GetBytes(got, "tool_choice.name").Exists() {
		t.Fatalf("flat tool_choice.name remains: %s", got)
	}
	assertJSONBool(t, got, "tools.0.function.strict", false)
	assertJSONBool(t, got, "tools.1.function.strict", true)
}

func TestApplyOpenAIResponsesChatFidelityPreservesUnrelatedChoicesAndExistingFields(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		translated string
	}{
		{
			name:       "required string",
			source:     `{"tool_choice":"required"}`,
			translated: `{"tool_choice":"required"}`,
		},
		{
			name:       "already nested named choice",
			source:     `{"tool_choice":{"type":"function","function":{"name":"lookup"}}}`,
			translated: `{"tool_choice":{"type":"function","function":{"name":"lookup"}}}`,
		},
		{
			name:       "absent fields",
			source:     `{"input":"hello"}`,
			translated: `{"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name:       "previous response id remains unsupported",
			source:     `{"input":"hello","previous_response_id":"resp_1"}`,
			translated: `{"messages":[{"role":"user","content":"hello"}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ApplyOpenAIResponsesChatFidelity([]byte(test.source), []byte(test.translated))
			if !bytes.Equal(got, []byte(test.translated)) {
				t.Fatalf("payload changed\n got: %s\nwant: %s", got, test.translated)
			}
		})
	}
}

func assertJSONBool(t *testing.T, payload []byte, path string, want bool) {
	t.Helper()
	result := gjson.GetBytes(payload, path)
	if !result.Exists() || result.Type != gjson.True && result.Type != gjson.False || result.Bool() != want {
		t.Fatalf("%s = %s, want %t; body=%s", path, result.Raw, want, payload)
	}
}
