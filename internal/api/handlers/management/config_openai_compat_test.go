package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetOpenAICompatIncludesResponsesChatFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	requestRetry := 0
	disableCooling := true
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "test-key"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "mimo-v2.5", Alias: "", MaxContextLength: 1_000_000, ResponsesChatPhaseBridge: true},
				},
				SupportPromptCacheKey: true,
ResponsesChatFidelity: true,
				DisableCooling:        &disableCooling,
				RequestRetry:          &requestRetry,
			},
		},
	}, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body struct {
		OpenAICompatibility []struct {
			SupportPromptCacheKey *bool `json:"support-prompt-cache-key"`
			ResponsesChatFidelity *bool `json:"responses-chat-fidelity"`
			DisableCooling        *bool `json:"disable-cooling"`
			RequestRetry          *int  `json:"request-retry"`
			Models                []struct {
				MaxContextLength         int   `json:"max-context-length"`
				ResponsesChatPhaseBridge *bool `json:"responses-chat-phase-bridge"`
			} `json:"models"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(body.OpenAICompatibility) != 1 {
		t.Fatalf("expected 1 openai-compatibility entry, got %d", len(body.OpenAICompatibility))
	}
	if body.OpenAICompatibility[0].SupportPromptCacheKey == nil || !*body.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatalf("expected support-prompt-cache-key to be present and true, got %#v", body.OpenAICompatibility[0].SupportPromptCacheKey)
	}
	if body.OpenAICompatibility[0].ResponsesChatFidelity == nil || !*body.OpenAICompatibility[0].ResponsesChatFidelity {
		t.Fatalf("expected responses-chat-fidelity to be present and true, got %#v", body.OpenAICompatibility[0].ResponsesChatFidelity)
	}
	if body.OpenAICompatibility[0].DisableCooling == nil || !*body.OpenAICompatibility[0].DisableCooling {
		t.Fatalf("expected disable-cooling to be present and true, got %#v", body.OpenAICompatibility[0].DisableCooling)
	}
	if body.OpenAICompatibility[0].RequestRetry == nil || *body.OpenAICompatibility[0].RequestRetry != 0 {
		t.Fatalf("expected request-retry to be present and 0, got %#v", body.OpenAICompatibility[0].RequestRetry)
	}
	if len(body.OpenAICompatibility[0].Models) != 1 {
		t.Fatalf("expected one model, got %#v", body.OpenAICompatibility[0].Models)
	}
	model := body.OpenAICompatibility[0].Models[0]
	if model.MaxContextLength != 1_000_000 {
		t.Fatalf("max-context-length = %d, want 1000000", model.MaxContextLength)
	}
	if model.ResponsesChatPhaseBridge == nil || !*model.ResponsesChatPhaseBridge {
		t.Fatalf("expected responses-chat-phase-bridge to be present and true, got %#v", model.ResponsesChatPhaseBridge)
	}
}

func TestPatchOpenAICompatRoundTripsResponsesChatFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("openai-compatibility: []\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	h := NewHandler(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "dashscope-intl",
		BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
	}}}, configPath, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{
		"index":0,
		"value":{
			"responses-chat-fidelity":true,
			"models":[{
				"name":"qwen3.8-max",
				"alias":"routine-standard",
				"max-context-length":1000000,
				"responses-chat-phase-bridge":true
			}]
		}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOpenAICompat(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	got := h.cfg.OpenAICompatibility[0]
	if !got.ResponsesChatFidelity || len(got.Models) != 1 || !got.Models[0].ResponsesChatPhaseBridge || got.Models[0].MaxContextLength != 1_000_000 {
		t.Fatalf("management round-trip lost Responses chat fields: %#v", got)
	}
}
