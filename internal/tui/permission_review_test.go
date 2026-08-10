package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

func TestPermissionReviewLifecycleIsAdvisoryAndBounded(t *testing.T) {
	app := newTestApp(100, 30)
	app.running = true

	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventPermissionReview,
		PermissionReview: &engine.PermissionReviewEvent{
			Phase:         engine.PermissionReviewChecking,
			CanonicalTool: "Read",
		},
	})
	if app.permissionReview != "Read" {
		t.Fatalf("checking state = %q", app.permissionReview)
	}
	if spinner := stripANSIForTest(app.renderSpinner()); !strings.Contains(
		spinner,
		"Advisory permission review checking Read",
	) {
		t.Fatalf("checking spinner = %q", spinner)
	}

	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventPermissionReview,
		PermissionReview: &engine.PermissionReviewEvent{
			Phase:         engine.PermissionReviewCompleted,
			CanonicalTool: "Read",
			Decision:      "approve",
			ReasonCode:    "expected_safe",
		},
	})
	if app.permissionReview != "" {
		t.Fatalf("completed state retained = %q", app.permissionReview)
	}
	active := app.notifications.Active()
	if len(active) != 1 ||
		active[0].Severity != NotifyInfo ||
		!strings.Contains(active[0].Message, "Advisory permission review completed") ||
		!strings.Contains(active[0].Message, "approve (expected_safe)") {
		t.Fatalf("completed notification = %#v", active)
	}
}

func TestPermissionReviewUnavailableDoesNotRenderUntrustedFields(t *testing.T) {
	app := newTestApp(100, 30)
	const secret = "secret-control"
	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventPermissionReview,
		PermissionReview: &engine.PermissionReviewEvent{
			Phase:         engine.PermissionReviewUnavailable,
			CanonicalTool: "Read\n" + secret,
			ReasonCode:    "timeout\n" + secret,
		},
	})
	active := app.notifications.Active()
	if len(active) != 1 || active[0].Severity != NotifyWarning {
		t.Fatalf("unavailable notification = %#v", active)
	}
	if strings.Contains(active[0].Message, secret) ||
		!strings.Contains(active[0].Message, "tool: unavailable") {
		t.Fatalf("unavailable message = %q", active[0].Message)
	}
}

func TestPermissionReviewPresentationFitsRepresentativeWidths(t *testing.T) {
	app := New(Config{Resumed: true, Model: "test-model"})
	app.running = true
	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventPermissionReview,
		PermissionReview: &engine.PermissionReviewEvent{
			Phase:         engine.PermissionReviewChecking,
			CanonicalTool: strings.Repeat("Read", 32),
		},
	})

	for _, width := range []int{40, 80, 120, 180} {
		updateAppSilent(app, tea.WindowSizeMsg{Width: width, Height: 30})
		for row, line := range strings.Split(app.renderView(), "\n") {
			if got := app.renderEnvironment.profile.width(line); got > width {
				t.Fatalf(
					"width=%d row=%d measured=%d line=%q",
					width,
					row,
					got,
					line,
				)
			}
			assertWidthProfileControlStateClosed(t, app.renderEnvironment.profile, line)
		}
	}
}
