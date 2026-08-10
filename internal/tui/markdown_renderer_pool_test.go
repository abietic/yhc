package tui

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"

	"github.com/abietic/yhc/engine"
)

func TestP412MarkdownRendererPoolBoundAndLRU(t *testing.T) {
	pool := newMarkdownRendererPool(2)
	env := defaultRenderEnvironment(defaultStyles())
	env.rendererPool = pool
	theme := markdownThemeForName(env.styles.theme)

	entryA := getRendererWithEnvironment(40, theme, env)
	entryB := getRendererWithEnvironment(41, theme, env)
	if entryA == nil || entryB == nil {
		t.Fatal("construct initial renderer entries")
	}
	if got := getRendererWithEnvironment(40, theme, env); got != entryA {
		t.Fatal("exact hit did not retain the indexed renderer entry")
	}
	entryC := getRendererWithEnvironment(42, theme, env)
	if entryC == nil {
		t.Fatal("construct renderer entry C")
	}
	if got := pool.stats(); got != (markdownRendererPoolStats{
		Size: 2, Capacity: 2, Creates: 3, Evictions: 1, PeakSize: 2,
	}) {
		t.Fatalf("stats after A/B/A/C = %#v", got)
	}

	entryBAgain := getRendererWithEnvironment(41, theme, env)
	if entryBAgain == nil || entryBAgain == entryB {
		t.Fatal("strict LRU did not evict and recreate entry B")
	}
	if got := pool.stats(); got != (markdownRendererPoolStats{
		Size: 2, Capacity: 2, Creates: 4, Evictions: 2, PeakSize: 2,
	}) {
		t.Fatalf("stats after recreating B = %#v", got)
	}
}

func TestP412MarkdownRendererPoolConstructionFailureIsNotCached(t *testing.T) {
	pool := newMarkdownRendererPool(2)
	attempts := 0
	pool.factory = func() (*glamour.TermRenderer, error) {
		attempts++
		return nil, errors.New("synthetic renderer failure")
	}
	env := defaultRenderEnvironment(defaultStyles())
	env.rendererPool = pool
	const source = "# heading\n\n- fallback"
	for range 2 {
		got := (&StreamingMarkdown{}).renderWithEnvironment(source, 40, env)
		want := renderSafeMarkdownLiteral(source, 40, env.profile)
		if got != want {
			t.Fatalf("failure fallback = %q, want %q", got, want)
		}
	}
	if attempts != 2 {
		t.Fatalf("construction attempts = %d, want 2 uncached attempts", attempts)
	}
	if got := pool.stats(); got != (markdownRendererPoolStats{Capacity: 2}) {
		t.Fatalf("failure mutated pool stats: %#v", got)
	}
}

func TestP412MarkdownRendererPoolCompatibilityStreamRetainsPrivatePool(t *testing.T) {
	stream := &StreamingMarkdown{}
	const source = "**compatibility** `stream`"
	for width := 32; width < 72; width++ {
		if got := stream.Render(source, width, ThemePolarNight); got == "" {
			t.Fatalf("empty compatibility render at width %d", width)
		}
	}
	pool := stream.compatibilityPool
	if pool == nil {
		t.Fatal("zero-value StreamingMarkdown did not retain a private pool")
	}
	stats := pool.stats()
	if stats.Capacity != defaultMarkdownRendererPoolCapacity ||
		stats.Size != defaultMarkdownRendererPoolCapacity ||
		stats.PeakSize != defaultMarkdownRendererPoolCapacity ||
		stats.Creates != 40 || stats.Evictions != 8 {
		t.Fatalf("compatibility pool stats = %#v", stats)
	}
}

func TestP412MarkdownRendererPoolChurnPreservesOutput(t *testing.T) {
	pool := newMarkdownRendererPool(3)
	env := defaultRenderEnvironment(StylesForTheme(ThemePolarNight))
	env.rendererPool = pool
	const source = "# Pool\n\n| A | B |\n| --- | --- |\n| **x** | `y` |"

	for generation := range 12 {
		env = env.withGeometry()
		if generation%2 == 1 {
			theme := ThemeDaybreak
			if env.styles.theme == ThemeDaybreak {
				theme = ThemePolarNight
			}
			env = env.withStyles(StylesForTheme(theme))
		}
		width := 36 + generation%4
		fresh := env
		fresh.rendererPool = newMarkdownRendererPool(3)

		got := (&StreamingMarkdown{}).renderWithEnvironment(source, width, env)
		want := (&StreamingMarkdown{}).renderWithEnvironment(source, width, fresh)
		if got != want {
			t.Fatalf("normal output changed at generation %d", generation)
		}
		gotSelection, gotOK := (&StreamingMarkdown{}).
			renderSelectionWithEnvironment(source, width, env)
		wantSelection, wantOK := (&StreamingMarkdown{}).
			renderSelectionWithEnvironment(source, width, fresh)
		if gotOK != wantOK || gotSelection != wantSelection {
			t.Fatalf("selection output changed at generation %d", generation)
		}
		if stats := pool.stats(); stats.Size > stats.Capacity || stats.PeakSize > stats.Capacity {
			t.Fatalf("pool exceeded bound at generation %d: %#v", generation, stats)
		}
	}
	if stats := pool.stats(); stats.Evictions == 0 || stats.Size != 3 || stats.PeakSize != 3 {
		t.Fatalf("churn did not exercise the hard bound: %#v", stats)
	}
}

func TestP412MarkdownRendererPoolInFlightEviction(t *testing.T) {
	pool := newMarkdownRendererPool(1)
	env := defaultRenderEnvironment(defaultStyles())
	env.rendererPool = pool
	theme := markdownThemeForName(env.styles.theme)
	oldEntry := getRendererWithEnvironment(40, theme, env)
	if oldEntry == nil {
		t.Fatal("construct old entry")
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	type renderResult struct {
		output string
		err    error
	}
	result := make(chan renderResult, 1)
	const source = "**retained** `entry`"
	go func() {
		oldEntry.mu.Lock()
		close(locked)
		<-release
		output, err := oldEntry.renderer.Render(source)
		oldEntry.mu.Unlock()
		result <- renderResult{output: output, err: err}
	}()
	<-locked
	if got := getRendererWithEnvironment(41, theme, env); got == nil {
		t.Fatal("construct evicting entry")
	}
	close(release)
	oldResult := <-result
	if oldResult.err != nil {
		t.Fatalf("old holder render: %v", oldResult.err)
	}

	recreated := getRendererWithEnvironment(40, theme, env)
	if recreated == nil || recreated == oldEntry {
		t.Fatal("evicted key did not receive a distinct indexed entry")
	}
	recreated.mu.Lock()
	newOutput, err := recreated.renderer.Render(source)
	recreated.mu.Unlock()
	if err != nil {
		t.Fatalf("recreated holder render: %v", err)
	}
	if oldResult.output != newOutput {
		t.Fatal("in-flight eviction changed renderer output")
	}
	if stats := pool.stats(); stats.Size != 1 || stats.Creates != 3 ||
		stats.Evictions != 2 || stats.PeakSize != 1 {
		t.Fatalf("in-flight eviction stats = %#v", stats)
	}
}

func TestP412MarkdownRendererPoolAppOwnershipSurvivesProjection(t *testing.T) {
	app := New(Config{Resumed: true})
	pool := app.renderEnvironment.rendererPool
	if pool == nil || pool.capacity != defaultMarkdownRendererPoolCapacity {
		t.Fatalf("initial App pool = %#v", pool)
	}
	assertPool := func(label string, got *markdownRendererPool) {
		t.Helper()
		if got != pool {
			t.Fatalf("%s pool = %p, want App pool %p", label, got, pool)
		}
	}

	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
	assertPool("resize", app.renderEnvironment.rendererPool)
	if err := app.applyTheme(string(ThemeDaybreak)); err != nil {
		t.Fatalf("apply theme: %v", err)
	}
	assertPool("theme", app.renderEnvironment.rendererPool)
	assertPool("chat", app.chat.environment.rendererPool)
	assertPool("thread store", app.threadViews.environment.rendererPool)
	assertPool("plan dialog", app.planDialog.environment.rendererPool)

	inactive, err := app.threadViews.activate("p412-inactive", engine.ThreadModeReplayOnly)
	if err != nil {
		t.Fatalf("activate inactive thread: %v", err)
	}
	assertPool("future thread", inactive.Chat.environment.rendererPool)
}

func TestP412MarkdownRendererPoolConcurrentAcquireRenderEvict(t *testing.T) {
	pool := newMarkdownRendererPool(2)
	base := defaultRenderEnvironment(defaultStyles())
	base.rendererPool = pool
	const workers = 12
	const iterations = 16
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := base
			for iteration := range iterations {
				env = env.withGeometry()
				width := 36 + (worker+iteration)%5
				theme := markdownThemeForName(env.styles.theme)
				entry := getRendererWithEnvironmentMode(
					width,
					theme,
					env,
					(worker+iteration)%2 == 0,
				)
				if entry == nil {
					errCh <- fmt.Errorf("worker %d iteration %d: nil entry", worker, iteration)
					return
				}
				entry.mu.Lock()
				_, err := entry.renderer.Render("**parallel** `renderer`")
				entry.mu.Unlock()
				if err != nil {
					errCh <- fmt.Errorf("worker %d iteration %d: %w", worker, iteration, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if stats := pool.stats(); stats.Size > 2 || stats.PeakSize > 2 || stats.Evictions == 0 {
		t.Fatalf("concurrent pool stats = %#v", stats)
	}
}

func BenchmarkP412MarkdownRendererPoolSteadyHit(b *testing.B) {
	pool := newMarkdownRendererPool(defaultMarkdownRendererPoolCapacity)
	env := defaultRenderEnvironment(defaultStyles())
	env.rendererPool = pool
	theme := markdownThemeForName(env.styles.theme)
	if getRendererWithEnvironment(80, theme, env) == nil {
		b.Fatal("construct steady renderer")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if getRendererWithEnvironment(80, theme, env) == nil {
			b.Fatal("steady renderer unavailable")
		}
	}
	b.StopTimer()
	stats := pool.stats()
	b.ReportMetric(float64(stats.PeakSize), "peak_entries")
	b.ReportMetric(float64(stats.Creates), "creates")
	b.ReportMetric(float64(stats.Evictions), "evictions")
}

func BenchmarkP412MarkdownRendererPoolGenerationChurn(b *testing.B) {
	const generationsPerOperation = 64
	var totalCreates uint64
	var totalEvictions uint64
	peakSize := 0
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		pool := newMarkdownRendererPool(defaultMarkdownRendererPoolCapacity)
		env := defaultRenderEnvironment(defaultStyles())
		env.rendererPool = pool
		theme := markdownThemeForName(env.styles.theme)
		for range generationsPerOperation {
			env = env.withGeometry()
			for _, selection := range []bool{false, true} {
				if getRendererWithEnvironmentMode(80, theme, env, selection) == nil {
					b.Fatal("churn renderer unavailable")
				}
			}
		}
		stats := pool.stats()
		totalCreates += stats.Creates
		totalEvictions += stats.Evictions
		if stats.PeakSize > peakSize {
			peakSize = stats.PeakSize
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(peakSize), "peak_entries")
	b.ReportMetric(float64(totalCreates)/float64(b.N), "creates/op")
	b.ReportMetric(float64(totalEvictions)/float64(b.N), "evictions/op")
}
