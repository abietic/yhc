package prefetch

import (
	"testing"

	"github.com/abietic/yhc/engine/skills"
)

func TestSkillPrefetchSkipsManualOnlySkills(t *testing.T) {
	registry := skills.NewSkillRegistry()
	registry.Register(&skills.Skill{Name: "manual-only", Content: "must not preload", DisableModelInvocation: true})
	prefetch := NewSkillPrefetch(registry)
	if got := prefetch.findMatchingSkills("manual-only"); len(got) != 0 {
		t.Fatalf("disabled skill prefetched: %d", len(got))
	}
}
