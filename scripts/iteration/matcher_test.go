package main

import "testing"

func TestMatchPathPattern(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
		wantErr bool
	}{
		{"engine/**/*.go", "engine/query.go", true, false},
		{"engine/**/*.go", "engine/session/store_test.go", true, false},
		{"**/*.md", "AGENTS.md", true, false},
		{"**/*.md", "docs/architecture/runtime/query-engine.md", true, false},
		{".codex/**", ".codex/hooks.json", true, false},
		{"tools/**/testdata/**", "tools/testdata/case/input.json", true, false},
		{"engine/**/*.go", "tools/read.go", false, false},
		{"../engine/**", "engine/query.go", false, true},
		{"/engine/**", "engine/query.go", false, true},
		{"engine\\**", "engine/query.go", false, true},
		{"", "engine/query.go", false, true},
	}
	for _, test := range tests {
		got, err := matchPathPattern(test.pattern, test.name)
		if (err != nil) != test.wantErr || got != test.want {
			t.Fatalf("matchPathPattern(%q, %q) = %v, %v", test.pattern, test.name, got, err)
		}
	}
}
