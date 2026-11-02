package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// maxComboContextLength is the highest accepted context window: 2147483647
// (the largest signed 32-bit integer). CPA converts plugin model metadata to
// int context lengths; staying within int32 keeps the value exact on every
// host platform, and a larger number is a typo or a missing unit rather than
// a real model limit. The dashboard mirrors this bound in its numeric input
// (see addContextSettings in dashboard.go).
const maxComboContextLength = int64(math.MaxInt32)

// parseContextLength converts one decoded YAML scalar into a usable token
// count. Anything that is not a positive integer within the bound is
// rejected as a value, never as a fatal configuration error.
func parseContextLength(value any) (int64, bool) {
	var n int64
	switch v := value.(type) {
	case int:
		n = int64(v)
	case int64:
		n = v
	case uint64:
		if v > uint64(maxComboContextLength) {
			return 0, false
		}
		n = int64(v)
	case float64:
		// Only exact whole numbers are token counts; YAML writes them bare.
		if v != math.Trunc(v) || math.IsInf(v, 0) || v > float64(maxComboContextLength) {
			return 0, false
		}
		n = int64(v)
	default:
		return 0, false
	}
	if n <= 0 || n > maxComboContextLength {
		return 0, false
	}
	return n, true
}

// sanitizedContextLengths splits configured context lengths into usable
// alias-to-token pairs plus two warning lists: entries whose alias or value
// is malformed, and entries for aliases that have no model chain (for
// example after a dashboard rename). Neither list is fatal: a rejected
// plugin.reconfigure would unregister every combo alias while CPA keeps the
// rejected YAML persisted, so a single stale or mistyped entry must never
// take working routing down. Entries are ignored with one warning line until
// an operator fixes or removes them.
func sanitizedContextLengths(cfg comboConfig) (usable map[string]int64, invalid, unknown []string) {
	usable = make(map[string]int64)
	if cfg.ContextLengths == nil {
		return usable, nil, nil
	}
	entries, ok := contextLengthEntries(cfg.ContextLengths)
	if !ok {
		// A scalar or sequence where a mapping belongs: ignore the whole
		// field rather than failing the reconfigure and unregistering
		// every combo alias.
		return usable, []string{fmt.Sprintf("<context_lengths is %T, expected a mapping>", cfg.ContextLengths)}, nil
	}
	for alias, raw := range entries {
		key := strings.TrimSpace(alias)
		length, okValue := parseContextLength(raw)
		if alias != key || key == "" || !okValue {
			invalid = append(invalid, fmt.Sprintf("%q=%v", alias, raw))
			continue
		}
		if _, chain := cfg.ModelChains[key]; !chain {
			unknown = append(unknown, key)
			continue
		}
		usable[key] = length
	}
	sort.Strings(invalid)
	sort.Strings(unknown)
	return usable, invalid, unknown
}

// contextLengthEntries accepts the two map shapes yaml.v3 can produce for an
// any-typed mapping: string keys for the normal case, interface{} keys when a
// key is not expressible as a string.
func contextLengthEntries(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			name, okString := key.(string)
			if !okString {
				name = fmt.Sprint(key)
				out[name] = item
				continue
			}
			out[name] = item
		}
		return out, true
	default:
		return nil, false
	}
}

// logCombo forwards a plugin log line through host.log. It never returns an
// error because logging must not break lifecycle or registration calls.
func logCombo(level, message string, fields map[string]any) {
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{"level": level, "message": message, "fields": fields})
}
