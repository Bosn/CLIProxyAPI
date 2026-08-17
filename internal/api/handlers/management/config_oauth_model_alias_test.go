package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOAuthModelAliasManagementPreservesMaxContextLength(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := NewHandler(&config.Config{}, writeTestConfigFile(t), nil)

	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/oauth-model-alias", strings.NewReader(`{
		"channel":"codex",
		"aliases":[{
			"name":"gpt-5.6-sol",
			"alias":"gpt-5.6-sol-large-context",
			"fork":true,
			"display-name":"GPT 5.6 Sol Large Context",
			"max-context-length":947369,
			"source-max-context-length":372000
		}]
	}`))
	patchCtx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOAuthModelAlias(patchCtx)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/oauth-model-alias", nil)
	h.GetOAuthModelAlias(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d; body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}

	var body struct {
		OAuthModelAlias map[string][]config.OAuthModelAlias `json:"oauth-model-alias"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	aliases := body.OAuthModelAlias["codex"]
	if len(aliases) != 1 {
		t.Fatalf("expected one codex alias, got %#v", aliases)
	}
	if aliases[0].MaxContextLength != 947369 {
		t.Fatalf("max-context-length = %d, want 947369", aliases[0].MaxContextLength)
	}
	if aliases[0].SourceMaxContextLength != 372000 {
		t.Fatalf("source-max-context-length = %d, want 372000", aliases[0].SourceMaxContextLength)
	}
}
