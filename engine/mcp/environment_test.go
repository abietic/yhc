package mcp

import "testing"

func TestCanonicalEnvironmentKeyForOS(t *testing.T) {
	tests := []struct {
		name string
		goos string
		key  string
		want string
	}{
		{name: "windows mixed case", goos: "windows", key: "Path", want: "PATH"},
		{name: "windows uppercase", goos: "windows", key: "PATH", want: "PATH"},
		{name: "linux exact", goos: "linux", key: "Path", want: "Path"},
		{name: "darwin exact", goos: "darwin", key: "Path", want: "Path"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := canonicalEnvironmentKeyForOS(test.goos, test.key); got != test.want {
				t.Fatalf(
					"canonicalEnvironmentKeyForOS(%q, %q) = %q, want %q",
					test.goos,
					test.key,
					got,
					test.want,
				)
			}
		})
	}
}
