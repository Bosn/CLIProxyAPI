package pluginhost

import (
	"context"
	"testing"
)

func TestHostStartLoginNilSafe(t *testing.T) {
	var nilHost *Host
	resp, handled, err := nilHost.StartLogin(context.Background(), "test", "http://example.com", map[string]any{"key": "val"})
	if handled || err != nil || resp.State != "" {
		t.Fatalf("StartLogin on nil host = (%#v, %v, %v), want zero values", resp, handled, err)
	}

	emptyHost := &Host{}
	resp, handled, err = emptyHost.StartLogin(context.Background(), "test", "http://example.com", map[string]any{"key": "val"})
	if handled || err != nil || resp.State != "" {
		t.Fatalf("StartLogin on empty host = (%#v, %v, %v), want zero values", resp, handled, err)
	}
}

func TestOAuthModelAliasToInternalPreservesContextOverrides(t *testing.T) {
	aliases := oauthModelAliasToInternal(map[string][]OAuthModelAlias{
		"codex": {
			{
				Name:                   "gpt-5.6-sol",
				Alias:                  "gpt-5.6-sol-large-context",
				Fork:                   true,
				MaxContextLength:       947369,
				SourceMaxContextLength: 372000,
			},
		},
	})

	got := aliases["codex"]
	if len(got) != 1 {
		t.Fatalf("aliases = %#v, want one codex alias", aliases)
	}
	if got[0].MaxContextLength != 947369 || got[0].SourceMaxContextLength != 372000 {
		t.Fatalf("context overrides = alias:%d source:%d, want alias:947369 source:372000", got[0].MaxContextLength, got[0].SourceMaxContextLength)
	}
}
