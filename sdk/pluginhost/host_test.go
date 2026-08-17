package pluginhost

import "testing"

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
