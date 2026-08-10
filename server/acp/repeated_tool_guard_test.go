package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestRepeatedToolPermissionOptionsAreOneShotOnly(t *testing.T) {
	options := repeatedToolPermissionOptions()
	if len(options) != 2 {
		t.Fatalf("option count = %d, want 2", len(options))
	}
	if options[0].Kind != acpsdk.PermissionOptionKindAllowOnce || string(options[0].OptionId) != "run_once" {
		t.Fatalf("allow option = %#v", options[0])
	}
	if options[1].Kind != acpsdk.PermissionOptionKindRejectOnce || string(options[1].OptionId) != "stop" {
		t.Fatalf("reject option = %#v", options[1])
	}
	for _, option := range options {
		if option.Kind == acpsdk.PermissionOptionKindAllowAlways {
			t.Fatalf("persistent option leaked into repeated-tool prompt: %#v", option)
		}
	}
}
