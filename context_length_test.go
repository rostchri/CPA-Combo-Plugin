package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// withRestoredConfig restores the plugin-wide configuration after a test so
// tests stay independent of execution order.
func withRestoredConfig(t *testing.T) {
	t.Helper()
	previous := currentConfig.Load()
	t.Cleanup(func() {
		if previous != nil {
			currentConfig.Store(previous)
			return
		}
		currentConfig.Store(comboConfig{})
	})
}

func configureForTest(t *testing.T, yaml string) error {
	t.Helper()
	raw, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte(yaml)})
	if err != nil {
		t.Fatal(err)
	}
	return configure(raw)
}

func TestConfigureParsesContextLengths(t *testing.T) {
	withRestoredConfig(t)
	if err := configureForTest(t, "enabled: true\nmodel_chains:\n  combo:\n    - model: m\ncontext_lengths:\n  combo: 524288\n"); err != nil {
		t.Fatalf("configure with context_lengths: %v", err)
	}
	lengths, invalid, unknown := sanitizedContextLengths(loadedConfig())
	if lengths["combo"] != 524288 || len(invalid) != 0 || len(unknown) != 0 {
		t.Fatalf("sanitized = %#v invalid=%v unknown=%v", lengths, invalid, unknown)
	}
}

func TestContextLengthsNeverFailReconfigure(t *testing.T) {
	withRestoredConfig(t)
	tests := []struct {
		name  string
		input string
	}{
		{name: "zero", input: "context_lengths:\n  combo: 0\n"},
		{name: "negative", input: "context_lengths:\n  combo: -1\n"},
		{name: "above int32", input: "context_lengths:\n  combo: 2147483648\n"},
		{name: "float", input: "context_lengths:\n  combo: 1024.5\n"},
		{name: "text", input: "context_lengths:\n  combo: large\n"},
		{name: "nested mapping", input: "context_lengths:\n  combo: {tokens: 1024}\n"},
		{name: "blank alias", input: "context_lengths:\n  \" \": 1024\n"},
		{name: "untrimmed alias", input: "context_lengths:\n  \" combo \": 1024\n"},
		{name: "scalar instead of mapping", input: "context_lengths: 7\n"},
		{name: "sequence instead of mapping", input: "context_lengths:\n  - 1\n"},
		{name: "unknown alias", input: "context_lengths:\n  ghost: 1024\n"},
		{name: "null value", input: "context_lengths:\n  combo:\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := testConfig()
			currentConfig.Store(before)
			// A rejected reconfigure would unregister every combo alias, so
			// none of these inputs may return an error.
			if err := configureForTest(t, "enabled: true\nmodel_chains:\n  combo:\n    - model: m\n"+tt.input); err != nil {
				t.Fatalf("configure must not fail on malformed context_lengths (%s): %v", tt.name, err)
			}
			cfg := loadedConfig()
			if !cfg.Enabled || len(cfg.ModelChains) != 1 {
				t.Fatalf("valid parts were dropped: %#v", cfg)
			}
			_, invalid, unknown := sanitizedContextLengths(cfg)
			if len(invalid)+len(unknown) == 0 && tt.name != "null value" {
				t.Fatalf("malformed entry (%s) was neither invalid nor unknown: %#v", tt.name, cfg.ContextLengths)
			}
		})
	}
}

func TestSanitizedContextLengthsMixesValidAndIgnored(t *testing.T) {
	withRestoredConfig(t)
	cfg := testConfig()
	cfg.ContextLengths = map[string]any{"combo-coding": 524288, "ghost": 4096, "combo-coding ": 4096, "combo-coding2": int64(-1)}
	usable, invalid, unknown := sanitizedContextLengths(cfg)
	if usable["combo-coding"] != 524288 {
		t.Fatalf("usable = %#v", usable)
	}
	if len(usable) != 1 {
		t.Fatalf("usable must contain only the valid entry: %#v", usable)
	}
	if len(invalid) != 2 || len(unknown) != 1 {
		t.Fatalf("invalid=%v unknown=%v", invalid, unknown)
	}
}

func TestRegisterModelsSetsConfiguredContextLength(t *testing.T) {
	withRestoredConfig(t)
	cfg := testConfig()
	cfg.ModelChains["combo-plain"] = []comboCandidate{{Model: "plain"}}
	cfg.ContextLengths = map[string]any{"combo-coding": 524288, "ghost": 1}
	currentConfig.Store(cfg)
	models := registeredModelInfo(t)
	byID := map[string]pluginapi.ModelInfo{}
	for _, model := range models {
		byID[model.ID] = model
	}
	if len(byID) != 2 {
		t.Fatalf("registered models = %#v", models)
	}
	if byID["combo-coding"].ContextLength != 524288 {
		t.Errorf("combo-coding ContextLength = %d, want 524288", byID["combo-coding"].ContextLength)
	}
	if byID["combo-plain"].ContextLength != 0 {
		t.Errorf("combo-plain ContextLength = %d, want 0 without a configured value", byID["combo-plain"].ContextLength)
	}
}

func TestRegisterModelsKeepsRoutingDespiteBadContextLengths(t *testing.T) {
	withRestoredConfig(t)
	cfg := testConfig()
	cfg.ContextLengths = map[string]any{"combo-coding": 524288, "stale-alias": -9}
	currentConfig.Store(cfg)
	models := registeredModelInfo(t)
	if len(models) != 1 || models[0].ID != "combo-coding" || models[0].ContextLength != 524288 {
		t.Fatalf("registration must keep the valid alias despite the bad entry: %#v", models)
	}
}

func TestRegisterModelsWithoutContextLengthsMatchesUpstream(t *testing.T) {
	withRestoredConfig(t)
	currentConfig.Store(testConfig())
	for _, model := range registeredModelInfo(t) {
		if model.ContextLength != 0 {
			t.Fatalf("alias %q ContextLength = %d, want 0 when context_lengths is absent", model.ID, model.ContextLength)
		}
	}
}

func registeredModelInfo(t *testing.T) []pluginapi.ModelInfo {
	t.Helper()
	raw, err := registerModels()
	if err != nil {
		t.Fatal(err)
	}
	var got envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var result pluginapi.ModelRegistrationResponse
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Models
}

func TestConfigureDefaultsStayTransactionalAfterContextLengths(t *testing.T) {
	// The upstream default/transactional test asserts the same behaviour for
	// max_attempts; here we make sure context_lengths never becomes fatal so
	// the existing transactional guard keeps applying to the remaining
	// fields only.
	withRestoredConfig(t)
	before := testConfig()
	currentConfig.Store(before)
	if err := configureForTest(t, "enabled: true\nmodel_chains:\n  combo:\n    - model: m\ncontext_lengths:\n  combo: 1024\n"); err != nil {
		t.Fatal(err)
	}
	loaded := loadedConfig()
	if loaded.MaxAttempts != 3 || !reflect.DeepEqual(loaded.RetryStatuses, []int{403, 429, 502, 503, 504}) {
		t.Fatalf("defaults changed: %#v", loaded)
	}
}
