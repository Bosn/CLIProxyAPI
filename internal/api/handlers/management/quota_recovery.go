package management

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	codexQuotaUsagePath           = "/backend-api/wham/usage"
	codexRateLimitConsumePath     = "/backend-api/wham/rate-limit-reset-credits/consume"
	codexQuotaRecoveryProbeEvery  = time.Hour
	codexQuotaRecoveryReadLimit   = 1 << 20
	codexQuotaRecoveryUserAgent   = "CLIProxyAPI"
	defaultCodexQuotaUsageBaseURL = "https://chatgpt.com"
)

// StartQuotaRecoveryMonitor periodically checks cooling Codex OAuth credentials
// against ChatGPT's usage endpoint. A successful UI/API usage reset changes the
// upstream state before the long Retry-After window previously observed by CPA.
func (h *Handler) StartQuotaRecoveryMonitor(ctx context.Context) {
	if h == nil {
		return
	}
	h.quotaRecoveryStartOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		go h.runQuotaRecoveryMonitor(ctx, codexQuotaRecoveryProbeEvery)
	})
}

func (h *Handler) runQuotaRecoveryMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	h.probeCoolingCodexQuota(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.probeCoolingCodexQuota(ctx)
		}
	}
}

func (h *Handler) probeCoolingCodexQuota(ctx context.Context) {
	if h == nil || ctx == nil {
		return
	}
	if !h.quotaRecoveryEnabled() {
		return
	}

	h.quotaRecoveryMu.Lock()
	defer h.quotaRecoveryMu.Unlock()

	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return
	}

	now := time.Now()
	for _, auth := range manager.List() {
		if !codexAuthNeedsQuotaRecovery(auth, now) {
			continue
		}
		auth.EnsureIndex()
		probeCtx, cancelProbe := context.WithTimeout(ctx, defaultAPICallTimeout)
		recovered, errProbe := h.probeCodexQuotaForAuth(probeCtx, auth.Clone())
		if errProbe != nil {
			cancelProbe()
			log.WithError(errProbe).WithField("auth_index", auth.Index).Warn("codex quota recovery probe failed")
			continue
		}
		if recovered && h.clearCodexQuotaForAuth(probeCtx, auth) {
			log.WithField("auth_index", auth.Index).Info("codex quota recovery observed; cooldown cleared")
		}
		cancelProbe()
	}
}

func (h *Handler) quotaRecoveryEnabled() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	return cfg == nil || !cfg.Home.Enabled
}

func (h *Handler) probeCodexQuotaForAuth(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	if h == nil || auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false, nil
	}

	token, errToken := h.resolveTokenForAuth(ctx, auth, "")
	if errToken != nil {
		return false, fmt.Errorf("resolve codex token: %w", errToken)
	}
	if token == "" {
		return false, fmt.Errorf("codex token unavailable")
	}

	endpoint := strings.TrimSpace(h.codexQuotaUsageURL)
	if endpoint == "" {
		endpoint = defaultCodexQuotaUsageBaseURL + codexQuotaUsagePath
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errRequest != nil {
		return false, fmt.Errorf("build codex usage probe: %w", errRequest)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", codexQuotaRecoveryUserAgent)

	client := &http.Client{
		Timeout:   defaultAPICallTimeout,
		Transport: h.apiCallTransport(auth, ""),
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return false, fmt.Errorf("codex usage probe request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("close codex usage probe response")
		}
	}()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, codexQuotaRecoveryReadLimit))
	if errRead != nil {
		return false, fmt.Errorf("read codex usage probe response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false, fmt.Errorf("codex usage probe status %d", resp.StatusCode)
	}
	return codexUsageResponseShowsRecovery(body), nil
}

func (h *Handler) reconcileCodexQuotaAfterAPICall(ctx context.Context, auth *coreauth.Auth, method string, target *url.URL, statusCode int, body []byte) {
	if h == nil || auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return
	}

	recovered := false
	switch {
	case isCodexRateLimitConsumeCall(method, target):
		recovered = true
	case isCodexUsageCall(method, target):
		recovered = codexUsageResponseShowsRecovery(body)
	}
	if !recovered || !h.clearCodexQuotaForAuth(ctx, auth) {
		return
	}
	auth.EnsureIndex()
	log.WithField("auth_index", auth.Index).Info("codex usage reset observed; cooldown cleared")
}

func (h *Handler) clearCodexQuotaForAuth(ctx context.Context, auth *coreauth.Auth) bool {
	if h == nil || auth == nil || !codexAuthNeedsQuotaRecovery(auth, time.Now()) {
		return false
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return false
	}

	_, _, errReset := manager.ResetQuota(ctx, auth.ID)
	if errReset != nil {
		log.WithError(errReset).Warn("reset codex quota cooldown")
		return false
	}
	return true
}

func codexAuthNeedsQuotaRecovery(auth *coreauth.Auth, now time.Time) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	if auth.Unavailable || auth.Quota.Exceeded || isFuture(auth.NextRetryAfter, now) {
		return true
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Unavailable || state.Quota.Exceeded || isFuture(state.NextRetryAfter, now) {
			return true
		}
	}
	return false
}

func isFuture(value time.Time, now time.Time) bool {
	return !value.IsZero() && value.After(now)
}

func isCodexRateLimitConsumeCall(method string, target *url.URL) bool {
	return target != nil && strings.EqualFold(strings.TrimSpace(method), http.MethodPost) && cleanURLPath(target.Path) == codexRateLimitConsumePath
}

func isCodexUsageCall(method string, target *url.URL) bool {
	return target != nil && strings.EqualFold(strings.TrimSpace(method), http.MethodGet) && cleanURLPath(target.Path) == codexQuotaUsagePath
}

func cleanURLPath(path string) string {
	path = strings.TrimSpace(path)
	if path != "" && path != "/" {
		path = strings.TrimRight(path, "/")
	}
	return path
}

func codexUsageResponseShowsRecovery(raw []byte) bool {
	if !gjson.ValidBytes(raw) {
		return false
	}

	modelUsage := gjson.GetBytes(raw, "model_usage")
	if modelUsage.IsObject() {
		for _, state := range modelUsage.Map() {
			if state.Get("available").Bool() {
				return true
			}
		}
	}

	for _, path := range []string{"rate_limit", "rateLimit", "code_review_rate_limit", "codeReviewRateLimit"} {
		if codexLimitShowsRecovery(gjson.GetBytes(raw, path)) {
			return true
		}
	}
	return false
}

func codexLimitShowsRecovery(limit gjson.Result) bool {
	if !limit.IsObject() {
		return false
	}
	reached, okReached := codexBooleanField(limit, "limit_reached", "limitReached")
	if !okReached {
		return false
	}
	if !reached {
		return true
	}
	return false
}

func codexBooleanField(value gjson.Result, names ...string) (bool, bool) {
	for _, name := range names {
		field := value.Get(name)
		if field.Type == gjson.True {
			return true, true
		}
		if field.Type == gjson.False {
			return false, true
		}
	}
	return false, false
}
