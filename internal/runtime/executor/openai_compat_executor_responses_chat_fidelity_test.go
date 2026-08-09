package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorResponsesChatFidelity(t *testing.T) {
	tests := []struct {
		name     string
		stream   bool
		enabled  bool
		wantPath string
	}{
		{name: "non-stream enabled", enabled: true, wantPath: "/v1/chat/completions"},
		{name: "stream enabled", stream: true, enabled: true, wantPath: "/v1/chat/completions"},
		{name: "disabled preserves native translation", wantPath: "/v1/chat/completions"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotPath string
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				if test.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()

			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
				Name:                  "dashscope-intl",
				ResponsesChatFidelity: test.enabled,
			}}}
			executor := NewOpenAICompatExecutor("dashscope-intl", cfg)
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url":     server.URL + "/v1",
					"api_key":      "test",
					"compat_name":  "dashscope-intl",
					"provider_key": "dashscope-intl",
				},
			}
			payload := []byte(`{
				"model":"deepseek-v4-flash-0731",
				"input":"hello",
				"stream":` + boolJSON(test.stream) + `,
				"tool_choice":{"type":"function","name":"lookup"},
				"tools":[{"type":"function","name":"lookup","description":"Lookup","parameters":{"type":"object"},"strict":false}],
				"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"},"strict":false}}
			}`)
			req := cliproxyexecutor.Request{Model: "deepseek-v4-flash-0731", Payload: payload}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAIResponse,
				ResponseFormat: sdktranslator.FormatOpenAIResponse,
				Stream:         test.stream,
			}

			if test.stream {
				result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream error: %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
				}
			} else {
				if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatalf("Execute error: %v", errExecute)
				}
			}

			if gotPath != test.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, test.wantPath)
			}
			if !test.enabled {
				native := helps.TranslateRequestWithCodexMultiAgentV2(context.Background(), nil, cfg, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, req.Model, payload, test.stream)
				for _, path := range []string{"tool_choice", "tools", "response_format"} {
					gotValue := gjson.GetBytes(gotBody, path)
					wantValue := gjson.GetBytes(native, path)
					if gotValue.Exists() != wantValue.Exists() || gotValue.Raw != wantValue.Raw {
						t.Fatalf("%s changed with fidelity disabled: got %s, want %s; body=%s", path, gotValue.Raw, wantValue.Raw, gotBody)
					}
				}
				return
			}

			if name := gjson.GetBytes(gotBody, "tool_choice.function.name").String(); name != "lookup" {
				t.Fatalf("tool_choice.function.name = %q, want lookup; body=%s", name, gotBody)
			}
			if gjson.GetBytes(gotBody, "tool_choice.name").Exists() {
				t.Fatalf("flat tool_choice.name remains: %s", gotBody)
			}
			if formatType := gjson.GetBytes(gotBody, "response_format.type").String(); formatType != "json_schema" {
				t.Fatalf("response_format.type = %q, want json_schema; body=%s", formatType, gotBody)
			}
			assertExecutorJSONBool(t, gotBody, "response_format.json_schema.strict", false)
			assertExecutorJSONBool(t, gotBody, "tools.0.function.strict", false)
		})
	}
}

func TestOpenAICompatExecutorResponsesChatFidelitySkipsCompact(t *testing.T) {
	executor := NewOpenAICompatExecutor("dashscope-intl", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "dashscope-intl", ResponsesChatFidelity: true}},
	})
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "dashscope-intl"}}
	source := []byte(`{"tool_choice":{"type":"function","name":"lookup"}}`)
	translated := append([]byte(nil), source...)

	got := executor.applyResponsesChatFidelity(auth, sdktranslator.FormatOpenAIResponse, "responses/compact", source, translated)
	if !bytes.Equal(got, translated) {
		t.Fatalf("compact payload changed: got %s, want %s", got, translated)
	}
}

func TestOpenAICompatExecutorResponsesChatFidelityDisabledReturnsPayloadUnchanged(t *testing.T) {
	executor := NewOpenAICompatExecutor("dashscope-intl", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "dashscope-intl"}},
	})
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "dashscope-intl"}}
	source := []byte(`{"tool_choice":{"type":"function","name":"lookup"},"text":{"format":{"type":"json_object"}}}`)
	translated := []byte(`{"tool_choice":{"type":"function","name":"lookup"},"messages":[]}`)

	got := executor.applyResponsesChatFidelity(auth, sdktranslator.FormatOpenAIResponse, "", source, translated)
	if !bytes.Equal(got, translated) {
		t.Fatalf("disabled fidelity changed payload bytes: got %s, want %s", got, translated)
	}
}

func TestOpenAICompatExecutorResponsesChatPhaseBridgeIsModelScoped(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "dashscope-intl",
		Models: []config.OpenAICompatibilityModel{
			{Name: "deepseek-v4-flash-0731", Alias: "routine-standard"},
			{Name: "qwen3.8-max", ResponsesChatPhaseBridge: true},
		},
	}}}
	executor := NewOpenAICompatExecutor("dashscope-intl", cfg)
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "dashscope-intl"}}
	source := []byte(`{"input":[{"role":"user","content":"hello"}]}`)
	translated := []byte(`{"messages":[{"role":"system","content":"base"},{"role":"user","content":"hello"}]}`)

	qwen := executor.applyResponsesChatPhaseBridge(auth, sdktranslator.FormatOpenAIResponse, "", "qwen3.8-max", source, translated)
	if bytes.Equal(qwen, translated) || !gjson.GetBytes(qwen, "messages.0.content").Exists() || !strings.Contains(gjson.GetBytes(qwen, "messages.0.content").String(), "strict execution contract") {
		t.Fatalf("qwen phase bridge was not applied: %s", qwen)
	}
	deepseek := executor.applyResponsesChatPhaseBridge(auth, sdktranslator.FormatOpenAIResponse, "", "deepseek-v4-flash-0731", source, translated)
	if !bytes.Equal(deepseek, translated) {
		t.Fatalf("deepseek payload changed: got %s, want %s", deepseek, translated)
	}
	compact := executor.applyResponsesChatPhaseBridge(auth, sdktranslator.FormatOpenAIResponse, "responses/compact", "qwen3.8-max", source, translated)
	if !bytes.Equal(compact, translated) {
		t.Fatalf("compact payload changed: got %s, want %s", compact, translated)
	}
}

func TestOpenAICompatExecutorRetriesProgressOnlyQwenTurnBeforeEmission(t *testing.T) {
	t.Parallel()
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requestCount.Add(1)
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("_cliproxy_responses_chat_phase_bridge")) || bytes.Contains(body, []byte("[[codex_")) {
			t.Errorf("internal phase marker leaked upstream: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_progress\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"qwen3.8-max\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Source checked. Next I'll inspect the live configuration.\"},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		if !bytes.Contains(body, []byte("previous generation ended with intermediate progress text")) {
			t.Errorf("retry instruction is missing: %s", body)
		}
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_retry\",\"object\":\"chat.completion.chunk\",\"created\":2,\"model\":\"qwen3.8-max\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"inspect\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "dashscope-intl",
		Models: []config.OpenAICompatibilityModel{{
			Name:                     "qwen3.8-max",
			ResponsesChatPhaseBridge: true,
		}},
	}}}
	executor := NewOpenAICompatExecutor("dashscope-intl", cfg)
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{
		"base_url":     server.URL + "/v1",
		"api_key":      "test",
		"compat_name":  "dashscope-intl",
		"provider_key": "dashscope-intl",
	}}
	payload := []byte(`{
		"model":"qwen3.8-max",
		"input":[{"role":"user","content":"Inspect the configuration and finish the task."}],
		"tools":[{"type":"function","name":"inspect","parameters":{"type":"object"}}],
		"tool_choice":"auto",
		"stream":true
	}`)
	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "qwen3.8-max", Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}
	var got strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("upstream request count = %d, want 2", requestCount.Load())
	}
	if strings.Contains(got.String(), "Source checked") {
		t.Fatalf("discarded progress response leaked downstream: %s", got.String())
	}
	if !strings.Contains(got.String(), `"type":"function_call"`) || !strings.Contains(got.String(), `"name":"inspect"`) {
		t.Fatalf("retry tool call is missing: %s", got.String())
	}
	if count := strings.Count(got.String(), "event: response.completed"); count != 1 {
		t.Fatalf("response.completed count = %d, want 1; stream=%s", count, got.String())
	}
}

func TestOpenAICompatExecutorDoesNotRetryQwenFinalAnswer(t *testing.T) {
	t.Parallel()
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_final\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"qwen3.8-max\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"The configuration is correct and no work remains.\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "dashscope-intl",
		Models: []config.OpenAICompatibilityModel{{
			Name:                     "qwen3.8-max",
			ResponsesChatPhaseBridge: true,
		}},
	}}}
	executor := NewOpenAICompatExecutor("dashscope-intl", cfg)
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{
		"base_url": server.URL + "/v1", "api_key": "test", "compat_name": "dashscope-intl",
	}}
	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.8-max",
		Payload: []byte(`{"model":"qwen3.8-max","input":"finish","tools":[{"type":"function","name":"inspect","parameters":{"type":"object"}}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}
	var got strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("upstream request count = %d, want 1", requestCount.Load())
	}
	if !strings.Contains(got.String(), "The configuration is correct and no work remains.") {
		t.Fatalf("final answer is missing: %s", got.String())
	}
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func assertExecutorJSONBool(t *testing.T, payload []byte, path string, want bool) {
	t.Helper()
	result := gjson.GetBytes(payload, path)
	if !result.Exists() || result.Type != gjson.True && result.Type != gjson.False || result.Bool() != want {
		t.Fatalf("%s = %s, want %t; body=%s", path, result.Raw, want, payload)
	}
}
