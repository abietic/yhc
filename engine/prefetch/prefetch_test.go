package prefetch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/compact"
	"github.com/cloudwego/eino/schema"
)

func TestPrefetchCacheExpirationEvictionOrderingAndInvalidation(t *testing.T) {
	cache := NewPrefetchCache(2, time.Hour)
	now := time.Now()
	cache.Set("low", &PrefetchItem{Type: TypeFile, Content: "low", Priority: 1, CachedAt: now})
	cache.Set("high", &PrefetchItem{Type: TypeGit, Content: "high", Priority: 10, CachedAt: now})
	cache.Set("mid", &PrefetchItem{Type: TypeMemory, Content: "mid", Priority: 5, CachedAt: now})

	if _, ok := cache.Get("low"); ok {
		t.Fatal("lowest-priority item should be evicted at capacity")
	}
	if got, ok := cache.Get("mid"); !ok || got.Content != "mid" {
		t.Fatalf("mid item missing: %#v ok=%v", got, ok)
	}
	cache.Set("mid", &PrefetchItem{Type: TypeMemory, Content: "updated", Priority: 0, CachedAt: now})
	if got, ok := cache.Get("mid"); !ok || got.Content != "updated" {
		t.Fatalf("replacement failed: %#v ok=%v", got, ok)
	}

	cache = NewPrefetchCache(0, time.Hour)
	cache.Set("mid", &PrefetchItem{Type: TypeMemory, Content: "mid", Priority: 5, CachedAt: now})
	cache.Set("high", &PrefetchItem{Type: TypeGit, Content: "high", Priority: 10, CachedAt: now})

	cache.Set("expired-ttl", &PrefetchItem{Type: TypeSkill, Content: "x", Priority: 20, CachedAt: now.Add(-2 * time.Hour), TTL: time.Minute})
	if _, ok := cache.Get("expired-ttl"); ok {
		t.Fatal("item-level TTL should expire")
	}
	cache.Set("expired-age", &PrefetchItem{Type: TypeSkill, Content: "x", Priority: 30, CachedAt: now.Add(-2 * time.Hour)})
	if _, ok := cache.Get("expired-age"); ok {
		t.Fatal("cache maxAge should expire old item")
	}
	cache.Prune()
	for _, key := range []string{"expired-ttl", "expired-age"} {
		if _, ok := cache.Get(key); ok {
			t.Fatalf("%s should have been pruned", key)
		}
	}

	items := cache.All()
	if len(items) != 2 || items[0].Priority < items[1].Priority {
		t.Fatalf("All should return valid items sorted by priority: %#v", items)
	}
	cache.InvalidateByType(TypeMemory)
	if _, ok := cache.Get("mid"); ok {
		t.Fatal("InvalidateByType should remove memory item")
	}
	cache.Invalidate("high")
	if _, ok := cache.Get("high"); ok {
		t.Fatal("Invalidate should remove key")
	}
}

func TestPrefetchRunnerCachesGitMemoryAndSkills(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("project memory\n"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills"), 0o755); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "writer.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	runner := NewPrefetchRunner(dir)
	if err := runner.RunPrefetch(context.Background()); err != nil {
		t.Fatalf("RunPrefetch failed: %v", err)
		return
	}
	results := runner.GetResults()
	byKey := map[string]*PrefetchItem{}
	for _, item := range results {
		byKey[item.Source] = item
	}
	if byKey["git status --porcelain"] == nil || !strings.Contains(byKey["git status --porcelain"].Content, "dirty.txt") {
		t.Fatalf("git status not cached: %#v", results)
		return
	}
	if byKey["git rev-parse --abbrev-ref HEAD"] == nil || byKey["git rev-parse --abbrev-ref HEAD"].Content == "" {
		t.Fatalf("git branch not cached: %#v", results)
		return
	}
	if byKey[filepath.Join(dir, "CLAUDE.md")] == nil || byKey[filepath.Join(dir, "CLAUDE.md")].Content != "project memory" {
		t.Fatalf("memory file not cached: %#v", results)
		return
	}
	if byKey[filepath.Join(dir, "skills")] == nil || !strings.Contains(byKey[filepath.Join(dir, "skills")].Content, "writer.md") {
		t.Fatalf("skill metadata not cached: %#v", results)
		return
	}
	if len(results) < 4 || results[0].Priority < results[len(results)-1].Priority {
		t.Fatalf("results should be priority sorted: %#v", results)
	}
}

func TestPrefetchRunnerSkipsMissingSourcesAndHonorsCanceledContext(t *testing.T) {
	dir := t.TempDir()
	runner := NewPrefetchRunner(dir)
	if err := runner.RunPrefetch(context.Background()); err != nil {
		t.Fatalf("missing git/memory/skills should be skipped, got %v", err)
		return
	}
	if len(runner.GetResults()) != 0 {
		t.Fatalf("empty project should have no results: %#v", runner.GetResults())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.RunPrefetch(ctx); err == nil || !strings.Contains(err.Error(), "memory: context canceled") {
		t.Fatalf("canceled context should surface memory prefetch error, got %v", err)
		return
	}
}

func TestMemoryPrefetchCollectsRecentEntriesWithMetadata(t *testing.T) {
	dir := t.TempDir()
	store := compact.NewMemoryStore(dir, 10)
	entries := []compact.MemoryEntry{
		{Category: "old", Content: "first", CreatedAt: time.Now().Add(-3 * time.Hour)},
		{Category: "new", Content: "second", CreatedAt: time.Now().Add(-2 * time.Hour)},
	}
	for _, entry := range entries {
		if err := store.Add(entry); err != nil {
			t.Fatal(err)
			return
		}
	}

	prefetch := NewMemoryPrefetch(store, 100)
	prefetch.Start([]*schema.Message{{Role: schema.User, Content: "ignored"}})
	result := prefetch.Collect()
	if len(result) != 1 {
		t.Fatalf("expected one memory attachment, got %#v", result)
	}
	msg := result[0]
	if msg.Role != schema.System || !strings.Contains(msg.Content, "[Session Memory") || !strings.Contains(msg.Content, "- [old] first") || !strings.Contains(msg.Content, "- [new] second") {
		t.Fatalf("unexpected memory message: %#v", msg)
	}
	if msg.Extra["is_meta"] != true || msg.Extra["attachment_kind"] != "memory_prefetch" {
		t.Fatalf("missing memory metadata: %#v", msg.Extra)
	}
	prefetch.Start(nil)
	if again := prefetch.Collect(); len(again) != 1 {
		t.Fatalf("second start should be ignored and preserve result: %#v", again)
	}
}

func TestMemoryPrefetchRanksLatestUserQueryBeforeRecency(t *testing.T) {
	dir := t.TempDir()
	store := compact.NewMemoryStore(dir, 10)
	now := time.Now()
	for _, entry := range []compact.MemoryEntry{
		{Category: "architecture", Content: "parser recovery uses bounded retries", CreatedAt: now.Add(-24 * time.Hour)},
		{Category: "recent", Content: "updated button colors", CreatedAt: now},
	} {
		if err := store.Add(entry); err != nil {
			t.Fatal(err)
		}
	}

	p := NewMemoryPrefetch(store, 100)
	p.Start([]*schema.Message{
		{Role: schema.User, Content: "button colors"},
		{Role: schema.Assistant, Content: "done"},
		{Role: schema.User, Content: "fix parser recovery"},
	})
	result := p.Collect()
	if len(result) != 1 {
		t.Fatalf("expected one memory attachment, got %#v", result)
	}
	content := result[0].Content
	relevant := strings.Index(content, "parser recovery")
	unrelated := strings.Index(content, "button colors")
	if relevant < 0 || unrelated < 0 || relevant > unrelated {
		t.Fatalf("older relevant memory should precede recent unrelated memory:\n%s", content)
	}
}

func TestMemoryPrefetchWithoutUserQueryFallsBackToRecency(t *testing.T) {
	store := compact.NewMemoryStore(t.TempDir(), 10)
	now := time.Now()
	_ = store.Add(compact.MemoryEntry{Category: "old", Content: "older", CreatedAt: now.Add(-time.Hour)})
	_ = store.Add(compact.MemoryEntry{Category: "new", Content: "newer", CreatedAt: now})

	p := NewMemoryPrefetch(store, 100)
	p.Start([]*schema.Message{{Role: schema.Assistant, Content: "no user query"}})
	content := p.Collect()[0].Content
	if strings.Index(content, "newer") > strings.Index(content, "older") {
		t.Fatalf("empty query should use newest-first fallback:\n%s", content)
	}
}

func TestMemoryPrefetchNilEmptyBudgetAndZeroValue(t *testing.T) {
	if got := (&MemoryPrefetch{}).Collect(); got != nil {
		t.Fatalf("zero-value collect should be nil, got %#v", got)
		return
	}
	nilStore := NewMemoryPrefetch(nil, 0)
	nilStore.Start(nil)
	if got := nilStore.Collect(); got != nil {
		t.Fatalf("nil store should produce no result, got %#v", got)
		return
	}

	store := compact.NewMemoryStore(t.TempDir(), 10)
	_ = store.Add(compact.MemoryEntry{Category: "cat", Content: strings.Repeat("x", 100), CreatedAt: time.Now()})
	prefetch := NewMemoryPrefetch(store, 1)
	prefetch.Start(nil)
	if got := prefetch.Collect(); got != nil {
		t.Fatalf("budget too small for any memory line should produce nil, got %#v", got)
		return
	}
}

func TestSkillPrefetchNilRegistryIsNoop(t *testing.T) {
	p := NewSkillPrefetch(nil)
	p.Start([]*schema.Message{{Role: schema.User, Content: "ignored"}})
	if got := p.Collect(); got != nil {
		t.Fatalf("SkillPrefetch with nil registry should be no-op, got %#v", got)
		return
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
		return
	}
}
