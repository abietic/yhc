package prefetch

import (
	"sync"
	"testing"
	"testing/synctest"

	"github.com/abietic/yhc/engine/skills"
	"github.com/cloudwego/eino/schema"
)

func TestSkillPrefetchImmediateCollectWaitsForStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := skills.NewSkillRegistry()
		registry.Register(&skills.Skill{Name: "trace-lens", Content: "independent skill content"})
		p := NewSkillPrefetch(registry)
		p.Start([]*schema.Message{{Role: schema.User, Content: "use trace-lens"}})
		// Do not yield to discovery first: Collect must join an accepted Start.
		messages := p.Collect()
		if len(messages) != 1 || messages[0].Content != "independent skill content" {
			t.Fatalf("accepted prefetch lost before worker ran: %#v", messages)
		}
	})
}

func TestSkillPrefetchEmptyCollectDoesNotSuppressLaterStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := skills.NewSkillRegistry()
		registry.Register(&skills.Skill{Name: "trace-lens", Content: "independent skill content"})
		p := NewSkillPrefetch(registry)
		p.Start([]*schema.Message{{Role: schema.Assistant, Content: "no user message"}})
		if messages := p.Collect(); messages != nil {
			t.Fatalf("unstarted prefetch returned content: %#v", messages)
		}
		p.Start([]*schema.Message{{Role: schema.User, Content: "trace-lens"}})
		synctest.Wait()
		messages := p.Collect()
		if len(messages) != 1 || messages[0].Content != "independent skill content" {
			t.Fatalf("empty collection disabled later start: %#v", messages)
		}
	})
}

func TestSkillPrefetchConcurrentCollectorsShareFirstStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registry := skills.NewSkillRegistry()
		registry.Register(&skills.Skill{Name: "first-lens", Content: "first accepted query"})
		registry.Register(&skills.Skill{Name: "second-lens", Content: "must not replace first"})
		p := NewSkillPrefetch(registry)
		p.Start([]*schema.Message{{Role: schema.User, Content: "first-lens"}})
		p.Start([]*schema.Message{{Role: schema.User, Content: "second-lens"}})
		const consumers = 16
		results := make(chan []*schema.Message, consumers)
		var group sync.WaitGroup
		for range consumers {
			group.Go(func() { results <- p.Collect() })
		}
		group.Wait()
		close(results)
		for messages := range results {
			if len(messages) != 1 || messages[0].Content != "first accepted query" {
				t.Fatalf("collector did not observe the first accepted query: %#v", messages)
			}
		}
	})
}

func TestSkillPrefetchWithoutStartIsNoop(t *testing.T) {
	for _, p := range []*SkillPrefetch{NewSkillPrefetch(nil), NewSkillPrefetch(skills.NewSkillRegistry()), {}} {
		if messages := p.Collect(); messages != nil {
			t.Fatalf("unstarted or zero-value prefetch returned content: %#v", messages)
		}
	}
}
