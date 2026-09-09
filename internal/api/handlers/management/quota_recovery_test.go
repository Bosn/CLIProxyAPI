package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const quotaRecoveryUsageBody = `{
	"plan_type":"pro",
	"rate_limit":{
		"allowed":true,
		"limit_reached":false,
		"primary_window":{"used_percent":6,"limit_window_seconds":604800}
	},
	"code_review_rate_limit":null,
	"additional_rate_limits":[],
	"model_usage":{"gpt-6-astra":{"available":true,"available_at":null}}
}`

func TestCodexUsageResponseShowsRecovery(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "rate limit reopened",
			body: quotaRecoveryUsageBody,
			want: true,
		},
		{
			name: "camel case rate limit reopened",
			body: `{"rateLimit":{"allowed":true,"limitReached":false}}`,
			want: true,
		},
		{
			name: "model available",
			body: `{"rate_limit":{"allowed":false,"limit_reached":true},"model_usage":{"gpt-6-astra":{"available":true}}}`,
			want: true,
		},
		{
			name: "still exhausted",
			body: `{"rate_limit":{"allowed":false,"limit_reached":true},"model_usage":{"gpt-6-astra":{"available":false}}}`,
			want: false,
		},
		{
			name: "no quota signal",
			body: `{"plan_type":"pro"}`,
			want: false,
		},
		{
			name: "invalid json",
			body: `not-json`,
			want: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := codexUsageResponseShowsRecovery([]byte(tc.body)); got != tc.want {
				t.Fatalf("codexUsageResponseShowsRecovery() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconcileCodexQuotaAfterAPICallClearsConsumeCooldown(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := coolingCodexTestAuth()
	registerCooldownTestAuth(t, manager, auth)

	h := &Handler{authManager: manager}
	target, errParse := url.Parse("https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume")
	if errParse != nil {
		t.Fatalf("parse URL: %v", errParse)
	}
	h.reconcileCodexQuotaAfterAPICall(context.Background(), auth, http.MethodPost, target, http.StatusOK, []byte(`{}`))

	assertCodexTestAuthCleared(t, manager, auth.ID)
}

func TestReconcileCodexQuotaAfterAPICallClearsUsageRecovery(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := coolingCodexTestAuth()
	registerCooldownTestAuth(t, manager, auth)

	h := &Handler{authManager: manager}
	target, errParse := url.Parse("https://chatgpt.com/backend-api/wham/usage")
	if errParse != nil {
		t.Fatalf("parse URL: %v", errParse)
	}
	h.reconcileCodexQuotaAfterAPICall(context.Background(), auth, http.MethodGet, target, http.StatusOK, []byte(quotaRecoveryUsageBody))

	assertCodexTestAuthCleared(t, manager, auth.ID)
}

func TestProbeCoolingCodexQuotaClearsRecoveredAuth(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer probe-token" {
			t.Errorf("Authorization = %q, want Bearer probe-token", got)
		}
		if got := r.Header.Get("User-Agent"); got != codexQuotaRecoveryUserAgent {
			t.Errorf("User-Agent = %q, want %q", got, codexQuotaRecoveryUserAgent)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(quotaRecoveryUsageBody))
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := coolingCodexTestAuth()
	auth.Metadata["access_token"] = "probe-token"
	registerCooldownTestAuth(t, manager, auth)

	h := NewHandler(&config.Config{}, "", manager)
	h.codexQuotaUsageURL = server.URL
	h.probeCoolingCodexQuota(context.Background())

	if got := requests.Load(); got != 1 {
		t.Fatalf("usage probe requests = %d, want 1", got)
	}
	assertCodexTestAuthCleared(t, manager, auth.ID)
}

func TestProbeCoolingCodexQuotaSkipsHealthyAuth(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(quotaRecoveryUsageBody))
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-healthy",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "probe-token"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandler(&config.Config{}, "", manager)
	h.codexQuotaUsageURL = server.URL
	h.probeCoolingCodexQuota(context.Background())

	if got := requests.Load(); got != 0 {
		t.Fatalf("usage probe requests = %d, want 0", got)
	}
}

func TestAPICallConsumeClearsCodexCooldown(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer probe-token" {
			t.Errorf("Authorization = %q, want Bearer probe-token", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := coolingCodexTestAuth()
	auth.Metadata["access_token"] = "probe-token"
	registerCooldownTestAuth(t, manager, auth)
	authIndex := auth.EnsureIndex()

	h := &Handler{authManager: manager, cfg: &config.Config{}}
	router := gin.New()
	router.POST("/api-call", h.APICall)

	requestBody, errMarshal := json.Marshal(map[string]any{
		"auth_index": authIndex,
		"method":     http.MethodPost,
		"url":        server.URL + codexRateLimitConsumePath,
		"header":     map[string]string{"Authorization": "Bearer $TOKEN$"},
		"data":       `{"redeem_request_id":"test"}`,
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api-call", strings.NewReader(string(requestBody)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	assertCodexTestAuthCleared(t, manager, auth.ID)
}

func coolingCodexTestAuth() *coreauth.Auth {
	deadline := time.Now().Add(time.Hour)
	return &coreauth.Auth{
		ID:             "codex-test",
		Provider:       "codex",
		Unavailable:    true,
		NextRetryAfter: deadline,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "usage_limit_reached",
			NextRecoverAt: deadline,
			BackoffLevel:  2,
		},
		ModelStates: map[string]*coreauth.ModelState{
			"gpt-6-astra": {
				Status:         coreauth.StatusError,
				StatusMessage:  "usage_limit_reached",
				Unavailable:    true,
				NextRetryAfter: deadline,
				Quota: coreauth.QuotaState{
					Exceeded:      true,
					Reason:        "usage_limit_reached",
					NextRecoverAt: deadline,
				},
			},
		},
		Metadata: map[string]any{"access_token": "probe-token"},
	}
}

func registerCooldownTestAuth(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
	t.Helper()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
}

func assertCodexTestAuthCleared(t *testing.T, manager *coreauth.Manager, authID string) {
	t.Helper()
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("auth %q not found", authID)
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() || updated.Quota.Exceeded || !updated.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("auth-level cooldown remains: unavailable=%v next=%v quota=%+v", updated.Unavailable, updated.NextRetryAfter, updated.Quota)
	}
	state := updated.ModelStates["gpt-6-astra"]
	if state == nil {
		t.Fatal("gpt-6-astra model state missing")
	}
	if state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded || !state.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("model cooldown remains: unavailable=%v next=%v quota=%+v", state.Unavailable, state.NextRetryAfter, state.Quota)
	}
	if updated.Status != coreauth.StatusActive || updated.StatusMessage != "" {
		t.Fatalf("auth status = %q message %q, want active/empty", updated.Status, updated.StatusMessage)
	}
}
