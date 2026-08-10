package cmd

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/provider"
)

func TestResolveProviderInputPreservesSourceLayers(t *testing.T) {
	flags := runtimeFlags{
		provider: "openai",
		model:    "gpt-4o",
		apiKey:   "flag-key",
		baseURL:  "https://flag.example/v1",
	}
	input := resolveProviderInput(&config.Config{
		Provider:   "anthropic",
		Model:      "claude-sonnet-4-6",
		APIBaseURL: "https://config.example",
		ModelAliases: map[string]string{
			"fast": "openai:gpt-4o-mini",
		},
	}, flags)
	input.Getenv = func(name string) string {
		values := map[string]string{"PROV": "deepseek", "PROV_API_KEY": "env-key"}
		return values[name]
	}
	input.CredentialLookup = func(string) (string, bool, error) { return "", false, nil }

	resolved, err := provider.ResolveConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != provider.ProviderAgenticOpenAI || resolved.Model != "gpt-4o" {
		t.Fatalf("resolved provider = %s:%s", resolved.Provider, resolved.Model)
	}
	if resolved.APIKey != "flag-key" || resolved.BaseURL != "https://flag.example/v1" {
		t.Fatalf("resolved credentials = key %q, base %q", resolved.APIKey, resolved.BaseURL)
	}
	if resolved.ModelAliases["fast"] != "openai:gpt-4o-mini" {
		t.Fatalf("model aliases = %#v", resolved.ModelAliases)
	}
}

func TestResolveFallbackModelPriority(t *testing.T) {
	appConfig := &config.Config{FallbackModel: "config-fallback"}
	flags := runtimeFlags{fallbackModel: "flag-fallback"}

	t.Setenv("PROV_FALLBACK_MODEL", "env-fallback")
	if got := resolveFallbackModel(appConfig, flags); got != "flag-fallback" {
		t.Fatalf("flag fallback = %q", got)
	}
	flags.fallbackModel = ""
	if got := resolveFallbackModel(appConfig, flags); got != "env-fallback" {
		t.Fatalf("environment fallback = %q", got)
	}
	t.Setenv("PROV_FALLBACK_MODEL", "")
	if got := resolveFallbackModel(appConfig, flags); got != "config-fallback" {
		t.Fatalf("config fallback = %q", got)
	}
}

func TestResolveApprovalReviewerRequiresExplicitOptInRoute(t *testing.T) {
	reviewer, err := resolveApprovalReviewer(
		context.Background(),
		runtimeFlags{
			approvalReviewProvider: "openai",
			approvalReviewModel:    "review-model",
		},
		io.Discard,
	)
	if err != nil || reviewer != nil {
		t.Fatalf("disabled reviewer = %#v, err=%v", reviewer, err)
	}

	for _, test := range []struct {
		name  string
		flags runtimeFlags
		want  string
	}{
		{
			name: "provider required",
			flags: runtimeFlags{
				approvalReviewShadow:  true,
				approvalReviewModel:   "review-model",
				approvalReviewTimeout: time.Second,
			},
			want: "explicit --permission-review-provider",
		},
		{
			name: "model required",
			flags: runtimeFlags{
				approvalReviewShadow:   true,
				approvalReviewProvider: "openai",
				approvalReviewTimeout:  time.Second,
			},
			want: "--permission-review-model",
		},
		{
			name: "positive timeout required",
			flags: runtimeFlags{
				approvalReviewShadow:   true,
				approvalReviewProvider: "openai",
				approvalReviewModel:    "review-model",
			},
			want: "timeout must be positive",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reviewer, err := resolveApprovalReviewer(
				context.Background(),
				test.flags,
				io.Discard,
			)
			if reviewer != nil || err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"reviewer = %#v, err=%v, want error containing %q",
					reviewer,
					err,
					test.want,
				)
			}
		})
	}
}

func TestResolveApprovalReviewAuditRequiresBothOptIns(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags runtimeFlags
		want  string
	}{
		{
			name: "directory without audit",
			flags: runtimeFlags{
				approvalReviewAuditDir: t.TempDir(),
			},
			want: "--permission-review-audit-dir requires",
		},
		{
			name: "audit without shadow",
			flags: runtimeFlags{
				approvalReviewAudit: true,
			},
			want: "--permission-review-shadow",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := resolveApprovalReviewAudit(test.flags, io.Discard)
			if store != nil || err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("store=%#v err=%v, want %q", store, err, test.want)
			}
		})
	}

	dir := t.TempDir() + "/audit"
	stderr := &strings.Builder{}
	store, err := resolveApprovalReviewAudit(runtimeFlags{
		approvalReviewShadow:   true,
		approvalReviewAudit:    true,
		approvalReviewAuditDir: dir,
	}, stderr)
	if err != nil || store == nil {
		t.Fatalf("enabled audit store=%#v err=%v", store, err)
	}
	if strings.Contains(stderr.String(), dir) ||
		!strings.Contains(stderr.String(), "local redacted size-window") {
		t.Fatalf("startup diagnostic = %q", stderr.String())
	}
}
