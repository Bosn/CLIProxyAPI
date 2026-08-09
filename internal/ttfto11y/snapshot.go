// Package ttfto11y projects usage records into a bounded, payload-free TTFT snapshot.
package ttfto11y

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	snapshotSchema        = "cliproxyapi.ttft-snapshot/v1"
	defaultMaxEvents      = 4096
	defaultRetention      = 48 * time.Hour
	maxSafeJSONInteger    = int64(1<<53 - 1)
	defaultSnapshotEnvKey = "CLIPROXYAPI_TTFT_SNAPSHOT_PATH"
)

type snapshotEvent struct {
	Seq             uint64 `json:"seq"`
	Ts              int64  `json:"ts"`
	CorrelationID   string `json:"correlationId"`
	SampleID        string `json:"sampleId"`
	Provider        string `json:"provider"`
	RouteID         string `json:"routeId"`
	ProviderTtftMs  int64  `json:"providerTtfbMs"`
	InputTokens     int64  `json:"inputTokens"`
	CacheReadTokens int64  `json:"cacheReadTokens"`
	OutputTokens    int64  `json:"outputTokens"`
	ReasoningTokens int64  `json:"reasoningTokens"`
	AccountedTokens int64  `json:"accountedTokens"`
	TerminalClass   string `json:"terminalClass"`
}

type snapshotDocument struct {
	SchemaVersion           string          `json:"schemaVersion"`
	SnapshotID              string          `json:"snapshotId"`
	StartedAtMs             int64           `json:"startedAtMs"`
	GeneratedAtMs           int64           `json:"generatedAtMs"`
	FirstSeq                uint64          `json:"firstSeq"`
	LastSeq                 uint64          `json:"lastSeq"`
	RetainedEventCount      int             `json:"retainedEventCount"`
	ObservedCount           uint64          `json:"observedCount"`
	EmittedCount            uint64          `json:"emittedCount"`
	MissingCorrelationCount uint64          `json:"missingCorrelationCount"`
	MissingSampleIDCount    uint64          `json:"missingSampleIdCount"`
	MissingTtftCount        uint64          `json:"missingTtftCount"`
	DuplicateSampleCount    uint64          `json:"duplicateSampleCount"`
	DroppedCount            uint64          `json:"droppedCount"`
	WriteFailureCount       uint64          `json:"writeFailureCount"`
	Events                  []snapshotEvent `json:"events"`
}

type snapshotPlugin struct {
	mu                      sync.Mutex
	path                    string
	now                     func() time.Time
	maxEvents               int
	retention               time.Duration
	snapshotID              string
	startedAtMs             int64
	nextSeq                 uint64
	events                  []snapshotEvent
	observedCount           uint64
	emittedCount            uint64
	missingCorrelationCount uint64
	missingSampleIDCount    uint64
	missingTtftCount        uint64
	duplicateSampleCount    uint64
	droppedCount            uint64
	writeFailureCount       uint64
	consecutiveWriteErrors  uint64
}

var registerDefaultOnce sync.Once

// RegisterDefault attaches the snapshot projector to the existing usage manager.
func RegisterDefault() {
	registerDefaultOnce.Do(func() {
		coreusage.RegisterNamedPlugin("cliproxyapi-ttft-o11y", newSnapshotPlugin(defaultSnapshotPath(), time.Now))
	})
}

func defaultSnapshotPath() string {
	if configured := strings.TrimSpace(os.Getenv(defaultSnapshotEnvKey)); configured != "" {
		if absolute, errAbs := filepath.Abs(configured); errAbs == nil {
			return absolute
		}
	}
	stateRoot := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if stateRoot == "" {
		home, errHome := os.UserHomeDir()
		if errHome != nil || strings.TrimSpace(home) == "" {
			stateRoot = filepath.Join(os.TempDir(), "cliproxyapi-state")
		} else {
			stateRoot = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(stateRoot, "cliproxyapi", "o11y", "ttft-snapshot.json")
}

func newSnapshotPlugin(path string, now func() time.Time) *snapshotPlugin {
	if now == nil {
		now = time.Now
	}
	startedAt := now().UnixMilli()
	return &snapshotPlugin{
		path:        path,
		now:         now,
		maxEvents:   defaultMaxEvents,
		retention:   defaultRetention,
		snapshotID:  newSnapshotID(startedAt),
		startedAtMs: startedAt,
		events:      make([]snapshotEvent, 0, defaultMaxEvents),
	}
}

func newSnapshotID(startedAt int64) string {
	buffer := make([]byte, 12)
	if _, errRead := rand.Read(buffer); errRead == nil {
		return hex.EncodeToString(buffer)
	}
	fallback := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", os.Getpid(), startedAt)))
	return hex.EncodeToString(fallback[:12])
}

func (p *snapshotPlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if p == nil || strings.TrimSpace(p.path) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	p.observedCount++
	p.pruneExpired(now)
	if !validCorrelationID(record.CorrelationID) {
		p.missingCorrelationCount++
		p.writeLocked(now)
		return
	}
	if !validSampleID(record.SampleID) {
		p.missingSampleIDCount++
		p.writeLocked(now)
		return
	}
	ttftMs := record.TTFT.Milliseconds()
	if record.TTFT > 0 && ttftMs == 0 {
		ttftMs = 1
	}
	if ttftMs <= 0 || ttftMs > maxSafeJSONInteger {
		p.missingTtftCount++
		p.writeLocked(now)
		return
	}
	for _, existing := range p.events {
		if existing.SampleID == record.SampleID {
			p.duplicateSampleCount++
			p.writeLocked(now)
			return
		}
	}

	p.nextSeq++
	event := snapshotEvent{
		Seq:             p.nextSeq,
		Ts:              now.UnixMilli(),
		CorrelationID:   record.CorrelationID,
		SampleID:        record.SampleID,
		Provider:        hashLabel(record.Provider),
		RouteID:         hashLabel(firstNonEmpty(record.Alias, record.Model)),
		ProviderTtftMs:  ttftMs,
		InputTokens:     boundedCount(record.Detail.InputTokens),
		CacheReadTokens: boundedCount(firstNonZero(record.Detail.CacheReadTokens, record.Detail.CachedTokens)),
		OutputTokens:    boundedCount(record.Detail.OutputTokens),
		ReasoningTokens: boundedCount(record.Detail.ReasoningTokens),
		AccountedTokens: boundedCount(record.Detail.TotalTokens),
		TerminalClass:   "completed",
	}
	if record.Failed {
		event.TerminalClass = "failed"
	}
	p.events = append(p.events, event)
	p.emittedCount++
	p.pruneCapacity()
	p.writeLocked(now)
}

func (p *snapshotPlugin) pruneExpired(now time.Time) {
	if p.retention <= 0 || len(p.events) == 0 {
		return
	}
	cutoff := now.Add(-p.retention).UnixMilli()
	retained := p.events[:0]
	for _, event := range p.events {
		if event.Ts < cutoff {
			p.droppedCount++
			continue
		}
		retained = append(retained, event)
	}
	p.events = retained
}

func (p *snapshotPlugin) pruneCapacity() {
	limit := p.maxEvents
	if limit <= 0 {
		limit = defaultMaxEvents
	}
	if len(p.events) <= limit {
		return
	}
	drop := len(p.events) - limit
	p.droppedCount += uint64(drop)
	copy(p.events, p.events[drop:])
	p.events = p.events[:limit]
}

func (p *snapshotPlugin) writeLocked(now time.Time) {
	firstSeq := uint64(0)
	if len(p.events) > 0 {
		firstSeq = p.events[0].Seq
	}
	document := snapshotDocument{
		SchemaVersion:           snapshotSchema,
		SnapshotID:              p.snapshotID,
		StartedAtMs:             p.startedAtMs,
		GeneratedAtMs:           now.UnixMilli(),
		FirstSeq:                firstSeq,
		LastSeq:                 p.nextSeq,
		RetainedEventCount:      len(p.events),
		ObservedCount:           p.observedCount,
		EmittedCount:            p.emittedCount,
		MissingCorrelationCount: p.missingCorrelationCount,
		MissingSampleIDCount:    p.missingSampleIDCount,
		MissingTtftCount:        p.missingTtftCount,
		DuplicateSampleCount:    p.duplicateSampleCount,
		DroppedCount:            p.droppedCount,
		WriteFailureCount:       p.writeFailureCount,
		Events:                  append([]snapshotEvent(nil), p.events...),
	}
	if errWrite := writeSnapshot(p.path, document); errWrite != nil {
		p.writeFailureCount++
		p.consecutiveWriteErrors++
		if p.consecutiveWriteErrors == 1 {
			log.Warn("TTFT O11Y snapshot write failed")
		}
		return
	}
	if p.consecutiveWriteErrors > 0 {
		log.Info("TTFT O11Y snapshot writes recovered")
	}
	p.consecutiveWriteErrors = 0
}

func writeSnapshot(path string, document snapshotDocument) error {
	directory := filepath.Dir(path)
	if errMkdir := os.MkdirAll(directory, 0o700); errMkdir != nil {
		return errMkdir
	}
	temporary, errCreate := os.CreateTemp(directory, ".ttft-snapshot-*")
	if errCreate != nil {
		return errCreate
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if errChmod := temporary.Chmod(0o600); errChmod != nil {
		_ = temporary.Close()
		return errChmod
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(document); errEncode != nil {
		_ = temporary.Close()
		return errEncode
	}
	if errSync := temporary.Sync(); errSync != nil {
		_ = temporary.Close()
		return errSync
	}
	if errClose := temporary.Close(); errClose != nil {
		return errClose
	}
	if errRename := os.Rename(temporaryPath, path); errRename != nil {
		return errRename
	}
	return os.Chmod(path, 0o600)
}

func validCorrelationID(value string) bool {
	if len(value) != 24 || strings.ToLower(value) != value {
		return false
	}
	decoded, errDecode := hex.DecodeString(value)
	return errDecode == nil && len(decoded) == 12
}

func validSampleID(value string) bool {
	if len(value) != 34 || !strings.HasPrefix(value, "s_") || strings.ToLower(value) != value {
		return false
	}
	decoded, errDecode := hex.DecodeString(value[2:])
	return errDecode == nil && len(decoded) == 16
}

func hashLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "unknown"
	}
	digest := sha256.Sum256([]byte(value))
	return "h_" + hex.EncodeToString(digest[:12])
}

func boundedCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	if value > maxSafeJSONInteger {
		return maxSafeJSONInteger
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
