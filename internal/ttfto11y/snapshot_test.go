package ttfto11y

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestSnapshotPersistsOnlyContentFreeAllowlistedFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "o11y", "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(path, func() time.Time { return now })
	record := coreusage.Record{
		Provider:      "secret-provider@example.test",
		Model:         "raw-model-metadata",
		Alias:         "client-route-private",
		APIKey:        "sk-super-secret-api-key",
		AuthID:        "private-auth-id",
		AuthIndex:     "private-auth-index",
		Source:        "customer@example.test",
		CorrelationID: "4bf92f3577b34da6a3ce929d",
		SampleID:      "s_c2fab392c1cde0e40d2f97bd1b385913",
		TTFT:          1500 * time.Millisecond,
		Failed:        true,
		Fail:          coreusage.Failure{StatusCode: 502, Body: "upstream failure body secret"},
		ResponseHeaders: http.Header{
			"Authorization": {"Bearer secret-token"},
			"Traceparent":   {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		},
		ReasoningEffort:     "private-effort-metadata",
		ServiceTier:         "private-service-tier",
		ResponseServiceTier: "private-response-tier",
		Detail: coreusage.Detail{
			InputTokens: 10, CacheReadTokens: 3, OutputTokens: 4, ReasoningTokens: 2, TotalTokens: 16,
		},
	}
	plugin.HandleUsage(context.Background(), record)

	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	serialized := string(raw)
	for _, forbidden := range []string{
		"secret-provider@example.test", "raw-model-metadata", "client-route-private",
		"sk-super-secret-api-key", "private-auth-id", "private-auth-index",
		"customer@example.test", "upstream failure body secret", "Bearer secret-token",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"apiKey", "failure body", "Authorization", "clientIp", "userAgent", "prompt",
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("snapshot contains forbidden value or field %q: %s", forbidden, serialized)
		}
	}
	var document snapshotDocument
	if errJSON := json.Unmarshal(raw, &document); errJSON != nil {
		t.Fatalf("Unmarshal() error = %v", errJSON)
	}
	if document.SchemaVersion != snapshotSchema || document.RetainedEventCount != 1 {
		t.Fatalf("unexpected snapshot metadata: %+v", document)
	}
	event := document.Events[0]
	if event.CorrelationID != record.CorrelationID || event.SampleID != record.SampleID || event.ProviderTtftMs != 1500 || event.TerminalClass != "failed" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if !strings.HasPrefix(event.Provider, "h_") || !strings.HasPrefix(event.RouteID, "h_") {
		t.Fatalf("provider and route must be one-way labels: %+v", event)
	}
	if event.InputTokens != 10 || event.CacheReadTokens != 3 || event.OutputTokens != 4 ||
		event.ReasoningTokens != 2 || event.AccountedTokens != 16 {
		t.Fatalf("unexpected bounded accounting: %+v", event)
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("Stat() error = %v", errStat)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStartupSnapshotAtomicallyReplacesAnOldGenerationWithoutModelTraffic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o11y", "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	first := newStartupSnapshotPlugin(path, func() time.Time { return now })
	firstDocument := readSnapshotDocument(t, path)
	assertSnapshotConserved(t, firstDocument)
	if firstDocument.SnapshotID != first.snapshotID || firstDocument.StartedAtMs != now.UnixMilli() ||
		firstDocument.GeneratedAtMs != now.UnixMilli() || firstDocument.RetainedEventCount != 0 ||
		firstDocument.ObservedCount != 0 || firstDocument.EmittedCount != 0 ||
		firstDocument.CounterLimitReached {
		t.Fatalf("unexpected zero-event startup snapshot: %+v", firstDocument)
	}

	now = now.Add(time.Second)
	second := newStartupSnapshotPlugin(path, func() time.Time { return now })
	secondDocument := readSnapshotDocument(t, path)
	assertSnapshotConserved(t, secondDocument)
	if secondDocument.SnapshotID != second.snapshotID ||
		secondDocument.SnapshotID == firstDocument.SnapshotID ||
		secondDocument.StartedAtMs != now.UnixMilli() || secondDocument.GeneratedAtMs != now.UnixMilli() ||
		secondDocument.RetainedEventCount != 0 || secondDocument.LastSeq != 0 {
		t.Fatalf("startup did not publish a fresh zero-event generation: %+v", secondDocument)
	}
}

func TestSnapshotDeduplicatesOneAttemptButRetainsDistinctRetrySpans(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(path, func() time.Time { return now })
	plugin.maxEvents = 2
	plugin.retention = time.Hour
	record := coreusage.Record{
		Provider: "openai", Model: "gpt-5.6-sol", CorrelationID: "4bf92f3577b34da6a3ce929d",
		SampleID: "s_c2fab392c1cde0e40d2f97bd1b385913",
		TTFT:     250 * time.Millisecond,
	}

	plugin.HandleUsage(context.Background(), record)
	plugin.HandleUsage(context.Background(), record)
	record.SampleID = "s_17b58d8757c41ab484d8e28f1d080f69"
	plugin.HandleUsage(context.Background(), record)
	document := readSnapshotDocument(t, path)
	if len(document.Events) != 2 || document.Events[0].Seq != 1 || document.Events[1].Seq != 2 {
		t.Fatalf("one attempt must dedupe while a retry span remains distinct: %+v", document.Events)
	}
	if document.DuplicateSampleCount != 1 || document.ObservedCount != 3 || document.EmittedCount != 2 {
		t.Fatalf("unexpected duplicate counters: %+v", document)
	}

	now = now.Add(2 * time.Hour)
	record.SampleID = "s_8a5182b11c8d6d2de90043df1a73a55a"
	plugin.HandleUsage(context.Background(), record)
	document = readSnapshotDocument(t, path)
	if len(document.Events) != 1 || document.Events[0].Seq != 3 {
		t.Fatalf("expired events were not removed: %+v", document.Events)
	}
	if document.DroppedCount != 2 || document.EmittedCount != 3 || document.ObservedCount != 4 {
		t.Fatalf("unexpected retention counters: %+v", document)
	}
}

func TestSnapshotCountsMissingCorrelationAndTTFTWithoutPersistingUsagePayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(path, func() time.Time { return now })
	plugin.HandleUsage(context.Background(), coreusage.Record{APIKey: "secret", Fail: coreusage.Failure{Body: "secret body"}})
	plugin.HandleUsage(context.Background(), coreusage.Record{
		CorrelationID: "4bf92f3577b34da6a3ce929d", SampleID: "s_c2fab392c1cde0e40d2f97bd1b385913",
		APIKey: "secret", Fail: coreusage.Failure{Body: "secret body"},
	})
	document := readSnapshotDocument(t, path)
	if document.MissingCorrelationCount != 1 || document.MissingTtftCount != 1 || len(document.Events) != 0 {
		t.Fatalf("unexpected missing-source counters: %+v", document)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "Body") {
		t.Fatalf("missing-source snapshot retained payload: %s", raw)
	}
}

func TestSnapshotSkipsNonGenerationWarmupWithoutReservingItsSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(path, func() time.Time { return now })
	record := coreusage.Record{
		Provider: "openai", Model: "gpt-5.6-sol", CorrelationID: "4bf92f3577b34da6a3ce929d",
		SampleID: "s_c2fab392c1cde0e40d2f97bd1b385913", TTFT: 250 * time.Millisecond,
		Generate: coreusage.GenerateFlag(false),
	}

	plugin.HandleUsage(context.Background(), record)
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("warmup wrote a native TTFT snapshot: %v", errStat)
	}

	record.Generate = coreusage.GenerateFlag(true)
	plugin.HandleUsage(context.Background(), record)
	document := readSnapshotDocument(t, path)
	if document.ObservedCount != 1 || document.EmittedCount != 1 ||
		document.SkippedWarmupCount != 1 ||
		document.DuplicateSampleCount != 0 || len(document.Events) != 1 {
		t.Fatalf("actual generation did not retain the sample after warmup: %+v", document)
	}
}

func TestSnapshotBoundsSkippedNonGenerationCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(path, func() time.Time { return now })
	plugin.skippedWarmupCount = uint64(maxSafeJSONInteger - 1)

	for range 2 {
		plugin.HandleUsage(context.Background(), coreusage.Record{
			Generate: coreusage.GenerateFlag(false),
		})
	}
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider: "openai", Model: "gpt-5.6-sol", CorrelationID: "4bf92f3577b34da6a3ce929d",
		SampleID: "s_c2fab392c1cde0e40d2f97bd1b385913", TTFT: 250 * time.Millisecond,
		Generate: coreusage.GenerateFlag(true),
	})

	document := readSnapshotDocument(t, path)
	if document.SkippedWarmupCount != uint64(maxSafeJSONInteger) {
		t.Fatalf("skipped non-generation count = %d, want bounded maximum %d",
			document.SkippedWarmupCount, maxSafeJSONInteger)
	}
}

func TestSnapshotSaturatesOneObservationBranchWithoutBreakingConservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(path, func() time.Time { return now })
	plugin.observedCount = uint64(maxSafeJSONInteger - 1)
	plugin.missingCorrelationCount = uint64(maxSafeJSONInteger - 1)

	plugin.HandleUsage(context.Background(), coreusage.Record{})
	document := readSnapshotDocument(t, path)
	assertSnapshotConserved(t, document)
	if document.ObservedCount != uint64(maxSafeJSONInteger) ||
		document.MissingCorrelationCount != uint64(maxSafeJSONInteger) {
		t.Fatalf("counters did not reach the exact safe ceiling: %+v", document)
	}
	if !document.CounterLimitReached {
		t.Fatalf("safe-counter saturation was not persisted: %+v", document)
	}
	plugin.HandleUsage(context.Background(), coreusage.Record{})
	after := readSnapshotDocument(t, path)
	if !after.CounterLimitReached || after.ObservedCount != document.ObservedCount {
		t.Fatalf("refused observation did not retain the persistent saturation marker: %+v", after)
	}
}

func TestSnapshotRefusesBeforeObservedWhenAClassificationCounterIsAlreadySaturated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(path, func() time.Time { return now })
	plugin.observedCount = uint64(maxSafeJSONInteger)
	plugin.missingCorrelationCount = uint64(maxSafeJSONInteger)

	plugin.HandleUsage(context.Background(), coreusage.Record{})
	document := readSnapshotDocument(t, path)
	assertSnapshotConserved(t, document)
	if !document.CounterLimitReached ||
		document.ObservedCount != uint64(maxSafeJSONInteger) ||
		document.MissingCorrelationCount != uint64(maxSafeJSONInteger) {
		t.Fatalf("saturated observation was not refused with a durable marker: %+v", document)
	}
}

func TestSnapshotReportsAWriteFailureOnTheNextSuccessfulSnapshot(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if errWrite := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(filepath.Join(blocked, "ttft-snapshot.json"), func() time.Time { return now })
	record := coreusage.Record{
		Provider: "openai", Model: "gpt-5.6-sol", CorrelationID: "4bf92f3577b34da6a3ce929d",
		SampleID: "s_c2fab392c1cde0e40d2f97bd1b385913", TTFT: 250 * time.Millisecond,
	}
	plugin.HandleUsage(context.Background(), record)
	if plugin.writeFailureCount != 1 || plugin.consecutiveWriteErrors != 1 {
		t.Fatalf("write failure was not retained in memory: %+v", plugin)
	}

	if errRemove := os.Remove(blocked); errRemove != nil {
		t.Fatalf("Remove() error = %v", errRemove)
	}
	if errMkdir := os.Mkdir(blocked, 0o700); errMkdir != nil {
		t.Fatalf("Mkdir() error = %v", errMkdir)
	}
	now = now.Add(time.Minute)
	record.SampleID = "s_17b58d8757c41ab484d8e28f1d080f69"
	plugin.HandleUsage(context.Background(), record)
	document := readSnapshotDocument(t, plugin.path)
	assertSnapshotConserved(t, document)
	if document.WriteFailureCount != 1 || document.RetainedEventCount != 2 ||
		document.ObservedCount != 2 || document.EmittedCount != 2 {
		t.Fatalf("recovery snapshot did not expose conserved write failure evidence: %+v", document)
	}
}

func TestSnapshotConservationHoldsAcrossDuplicateMissingAndDropPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttft-snapshot.json")
	now := time.Date(2026, time.August, 9, 8, 30, 0, 0, time.UTC)
	plugin := newSnapshotPlugin(path, func() time.Time { return now })
	plugin.maxEvents = 1
	record := coreusage.Record{
		Provider: "openai", Model: "gpt-5.6-sol", CorrelationID: "4bf92f3577b34da6a3ce929d",
		SampleID: "s_c2fab392c1cde0e40d2f97bd1b385913", TTFT: 250 * time.Millisecond,
	}
	plugin.HandleUsage(context.Background(), record)
	plugin.HandleUsage(context.Background(), record)
	record.SampleID = "s_17b58d8757c41ab484d8e28f1d080f69"
	plugin.HandleUsage(context.Background(), record)
	plugin.HandleUsage(context.Background(), coreusage.Record{})
	plugin.HandleUsage(context.Background(), coreusage.Record{
		CorrelationID: "4bf92f3577b34da6a3ce929d", TTFT: 250 * time.Millisecond,
	})
	plugin.HandleUsage(context.Background(), coreusage.Record{
		CorrelationID: "4bf92f3577b34da6a3ce929d",
		SampleID:      "s_8a5182b11c8d6d2de90043df1a73a55a",
	})
	document := readSnapshotDocument(t, path)
	assertSnapshotConserved(t, document)
	if document.DuplicateSampleCount != 1 || document.MissingCorrelationCount != 1 ||
		document.MissingSampleIDCount != 1 || document.MissingTtftCount != 1 ||
		document.DroppedCount != 1 || document.RetainedEventCount != 1 {
		t.Fatalf("unexpected path counters: %+v", document)
	}
}

func assertSnapshotConserved(t *testing.T, document snapshotDocument) {
	t.Helper()
	if document.ObservedCount != document.EmittedCount+document.MissingCorrelationCount+
		document.MissingSampleIDCount+document.MissingTtftCount+document.DuplicateSampleCount {
		t.Fatalf("observation counters are not conserved: %+v", document)
	}
	if document.EmittedCount != document.DroppedCount+uint64(document.RetainedEventCount) {
		t.Fatalf("emission counters are not conserved: %+v", document)
	}
	if document.LastSeq != document.EmittedCount {
		t.Fatalf("last sequence does not equal emitted count: %+v", document)
	}
}

func readSnapshotDocument(t *testing.T, path string) snapshotDocument {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var document snapshotDocument
	if errJSON := json.Unmarshal(raw, &document); errJSON != nil {
		t.Fatalf("Unmarshal() error = %v", errJSON)
	}
	return document
}
