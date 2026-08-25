package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/abietic/yhc/internal/buildinfo"
)

func TestBootstrapBuildIdentityIsBounded(t *testing.T) {
	identity := bootstrapBuildIdentity(buildinfo.Info{
		SchemaVersion: 1,
		Version:       "1.2.3",
		Commit:        "ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		BuildTime:     "2026-08-20T00:00:00Z",
		Modified:      true,
		GoVersion:     "go-test",
		OS:            "test-os",
		Arch:          "test-arch",
		Dependencies: []buildinfo.Dependency{{
			Path: "private-dependency", Version: "v1.0.0",
		}},
	})
	want := BuildIdentity{Version: "1.2.3", Commit: "abcdef012345", Modified: true}
	if identity != want {
		t.Fatalf("build identity = %#v, want %#v", identity, want)
	}

	payload, err := json.Marshal(Bootstrap{Build: identity})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"schema_version", "build_time", "go_version", "test-os", "test-arch",
		"dependencies", "private-dependency",
	} {
		if bytes.Contains(payload, []byte(forbidden)) {
			t.Fatalf("bootstrap leaked %q: %s", forbidden, payload)
		}
	}
}

func TestBootstrapBuildIdentityNormalizesCommit(t *testing.T) {
	tests := []struct {
		name   string
		commit string
		want   string
	}{
		{name: "unknown", commit: " unknown ", want: "unknown"},
		{name: "short hex", commit: "0123456789a", want: "unknown"},
		{name: "non hex", commit: "0123456789ag", want: "unknown"},
		{name: "too long", commit: strings.Repeat("a", 65), want: "unknown"},
		{name: "full uppercase", commit: strings.Repeat("ABCDEF01", 8), want: "abcdef01abcd"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity := bootstrapBuildIdentity(buildinfo.Info{
				Version: "1.2.3",
				Commit:  test.commit,
			})
			if identity.Commit != test.want {
				t.Fatalf("commit = %q, want %q", identity.Commit, test.want)
			}
		})
	}
}

func TestBootstrapBuildIdentityBoundsVersion(t *testing.T) {
	for _, test := range []struct {
		name    string
		version string
		want    string
	}{
		{name: "canonical", version: " 1.2.3-beta.1+build.4 ", want: "1.2.3-beta.1+build.4"},
		{name: "leading v", version: "v1.2.3", want: "unknown"},
		{name: "control character", version: "1.2.3\nspoof", want: "unknown"},
		{name: "too long", version: "1" + strings.Repeat("a", 64), want: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity := bootstrapBuildIdentity(buildinfo.Info{
				Version: test.version,
				Commit:  "unknown",
			})
			if identity.Version != test.want {
				t.Fatalf("version = %q, want %q", identity.Version, test.want)
			}
		})
	}
}

func TestBootstrapForIncludesCurrentProcessBuildIdentity(t *testing.T) {
	server, err := New(Config{
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	bootstrap, err := server.BootstrapFor(listener)
	if err != nil {
		t.Fatal(err)
	}
	if want := bootstrapBuildIdentity(buildinfo.Current()); bootstrap.Build != want {
		t.Fatalf("bootstrap build = %#v, want %#v", bootstrap.Build, want)
	}
}
