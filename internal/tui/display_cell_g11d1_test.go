package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/session"
)

func sameG11D1Environment(a, b RenderEnvironment) bool {
	return a.identity() == b.identity()
}

func g11D1Profile(tabStop int) DisplayCellProfile {
	policy := defaultDisplayCellPolicy()
	policy.TabStop = tabStop
	return newDisplayCellProfile(policy)
}

func assertG11D1Environment(
	t *testing.T,
	label string,
	got, want RenderEnvironment,
) {
	t.Helper()
	if !sameG11D1Environment(got, want) {
		t.Fatalf("%s environment = %#v, want %#v", label, got.identity(), want.identity())
	}
}

func TestG11D1EnvironmentGenerationsAreIndependent(t *testing.T) {
	env := defaultRenderEnvironment(defaultStyles())
	if env.themeGen == 0 || env.geometryGen == 0 {
		t.Fatalf("initial generations must be non-zero: %#v", env.identity())
	}
	themed := env.withStyles(StylesForTheme(ThemeDaybreak))
	if themed.themeGen != env.themeGen+1 ||
		themed.geometryGen != env.geometryGen ||
		themed.profile.Identity() != env.profile.Identity() {
		t.Fatalf(
			"theme update identity = %#v, want theme-only advance from %#v",
			themed.identity(),
			env.identity(),
		)
	}
	resized := themed.withGeometry()
	if resized.geometryGen != themed.geometryGen+1 ||
		resized.themeGen != themed.themeGen ||
		resized.profile.Identity() != themed.profile.Identity() {
		t.Fatalf(
			"geometry update identity = %#v, want geometry-only advance from %#v",
			resized.identity(),
			themed.identity(),
		)
	}
}

func TestG11D1RendererStableAndFullCachesBindExactEnvironment(t *testing.T) {
	const source = "intro\n\n| A | B |\n| --- | --- |\n| **x** | `y` |\n\nmutable tail"
	env := defaultRenderEnvironment(defaultStyles())
	stream := &StreamingMarkdown{}
	stream.renderWithEnvironment(source, 40, env)
	for name, identity := range map[string]markdownRenderIdentity{
		"stable": stream.stableCacheIdentity,
		"full":   stream.fullCacheIdentity,
	} {
		if identity.displayCellProfileID != env.profile.Identity() ||
			identity.themeGen != env.themeGen ||
			identity.geometryGen != env.geometryGen {
			t.Fatalf("%s cache identity = %#v, want %#v", name, identity, env.identity())
		}
	}

	firstRenderer := getRendererWithEnvironment(
		40,
		markdownThemeForName(env.styles.theme),
		env,
	)
	resized := env.withGeometry()
	stream.renderWithEnvironment(source, 40, resized)
	if stream.stableCacheIdentity.geometryGen != resized.geometryGen ||
		stream.fullCacheIdentity.geometryGen != resized.geometryGen {
		t.Fatalf(
			"geometry change retained old stable/full identity: stable=%#v full=%#v",
			stream.stableCacheIdentity,
			stream.fullCacheIdentity,
		)
	}
	if firstRenderer == getRendererWithEnvironment(
		40,
		markdownThemeForName(resized.styles.theme),
		resized,
	) {
		t.Fatal("renderer cache reused a distinct geometry generation")
	}

	rethemed := env.withStyles(env.styles)
	if firstRenderer == getRendererWithEnvironment(
		40,
		markdownThemeForName(rethemed.styles.theme),
		rethemed,
	) {
		t.Fatal("renderer cache reused a distinct theme generation")
	}

	profiled := env
	profiled.profile = g11D1Profile(8)
	stream.renderWithEnvironment(source, 40, profiled)
	if stream.stableCacheIdentity.displayCellProfileID != profiled.profile.Identity() ||
		stream.fullCacheIdentity.displayCellProfileID != profiled.profile.Identity() {
		t.Fatalf(
			"profile change retained old stable/full identity: stable=%#v full=%#v",
			stream.stableCacheIdentity,
			stream.fullCacheIdentity,
		)
	}
	if firstRenderer == getRendererWithEnvironment(
		40,
		markdownThemeForName(profiled.styles.theme),
		profiled,
	) {
		t.Fatal("renderer cache reused a distinct display-cell profile")
	}

	stream.Finalize(source)
	stream.renderWithEnvironment(source, 40, profiled)
	if stream.fullCacheIdentity.completeness != markdownFinalizedComplete ||
		stream.fullCacheIdentity.source != source {
		t.Fatalf("finalized cache identity = %#v", stream.fullCacheIdentity)
	}
}

func TestG11D1AppProjectsEnvironmentAcrossViewLifecycles(t *testing.T) {
	profile := g11D1Profile(8)
	app := New(Config{Resumed: true, DisplayCellProfile: &profile})
	initial := app.renderEnvironment
	assertG11D1Environment(t, "active chat", app.chat.environment, initial)
	assertG11D1Environment(t, "plan dialog", app.planDialog.environment, initial)
	assertG11D1Environment(t, "thread store", app.threadViews.environment, initial)

	inactive, err := app.threadViews.activate("inactive", engine.ThreadModeReplayOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.threadViews.activate(fallbackLeaderThreadID, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	assertG11D1Environment(t, "inactive chat", inactive.Chat.environment, initial)

	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
	resized := app.renderEnvironment
	if resized.geometryGen != initial.geometryGen+1 ||
		resized.themeGen != initial.themeGen {
		t.Fatalf("resize identity = %#v, want geometry-only advance", resized.identity())
	}
	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
	if app.renderEnvironment.geometryGen != resized.geometryGen {
		t.Fatal("identical terminal size advanced geometry generation")
	}
	assertG11D1Environment(t, "resized inactive chat", inactive.Chat.environment, resized)

	if err := app.applyTheme(string(ThemeDaybreak)); err != nil {
		t.Fatal(err)
	}
	themed := app.renderEnvironment
	if themed.themeGen != resized.themeGen+1 ||
		themed.geometryGen != resized.geometryGen {
		t.Fatalf("theme identity = %#v, want theme-only advance", themed.identity())
	}
	assertG11D1Environment(t, "themed inactive chat", inactive.Chat.environment, themed)
	assertG11D1Environment(t, "themed plan dialog", app.planDialog.environment, themed)
	app.planDialog.plan = "| A |\n| --- |\n| 🏷 |"
	app.planDialog.renderPlanMarkdown(40, 10)
	if app.planDialog.md.fullCacheIdentity.displayCellProfileID != profile.Identity() ||
		app.planDialog.md.fullCacheIdentity.themeGen != themed.themeGen ||
		app.planDialog.md.fullCacheIdentity.geometryGen != themed.geometryGen {
		t.Fatalf(
			"plan Markdown identity = %#v, want %#v",
			app.planDialog.md.fullCacheIdentity,
			themed.identity(),
		)
	}

	future, err := app.threadViews.activate("future", engine.ThreadModeReplayOnly)
	if err != nil {
		t.Fatal(err)
	}
	assertG11D1Environment(t, "future chat", future.Chat.environment, themed)

	restored := &threadViewState{
		ThreadID:  "restored",
		Mode:      engine.ThreadModeReplayOnly,
		Chat:      NewChatView(defaultStyles()),
		Search:    NewSearchOverlay(defaultStyles()),
		Selection: &Selection{},
		Surface:   StateChat,
	}
	app.restoreThreadView(restored)
	assertG11D1Environment(t, "restored chat", restored.Chat.environment, themed)
}

func TestG11D1DurableResetUsesAppEnvironment(t *testing.T) {
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "g11d1-durable",
		ThreadID:      "leader",
		CWD:           dir,
		TranscriptDir: filepath.Join(dir, "transcripts"),
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "| A | B |\n| --- | --- |\n| **x** | `y` |",
	}})
	if err := session.SaveSessionViewState(
		eng.GetTranscriptDir(),
		eng.SessionID(),
		session.PersistedSessionViewState{
			SessionID:      eng.SessionID(),
			ActiveThreadID: "leader",
			Threads: []session.PersistedThreadViewState{{
				ThreadID: "leader",
				Follow:   true,
			}},
		},
	); err != nil {
		t.Fatal(err)
	}

	profile := g11D1Profile(8)
	app := New(Config{
		Engine:             eng,
		Resumed:            true,
		DisplayCellProfile: &profile,
	})
	if err := app.resetAndRestoreSessionViews(); err != nil {
		t.Fatal(err)
	}
	assertG11D1Environment(
		t,
		"durable active chat",
		app.chat.environment,
		app.renderEnvironment,
	)
	assertG11D1Environment(
		t,
		"durable thread store",
		app.threadViews.environment,
		app.renderEnvironment,
	)
	app.chat.SetSize(80, 8)
	app.chat.Render(80, 8)
	foundAssistant := false
	for _, item := range app.chat.items {
		assistant, ok := item.(*AssistantMessage)
		if !ok {
			continue
		}
		foundAssistant = true
		if assistant.streamingMd.fullCacheIdentity.displayCellProfileID != profile.Identity() {
			t.Fatalf(
				"restored assistant profile = %q, want %q",
				assistant.streamingMd.fullCacheIdentity.displayCellProfileID,
				profile.Identity(),
			)
		}
	}
	if !foundAssistant {
		t.Fatal("durable reset did not hydrate an assistant item")
	}
}

func TestG11D1AppProjectsEnvironmentIntoCompatibilityStreamingMessage(t *testing.T) {
	profile := g11D1Profile(8)
	app := New(Config{Resumed: true, DisplayCellProfile: &profile})
	message := NewStreamingMessage(defaultStyles())
	message.Start()
	message.AppendContent("| A | B |\n| --- | --- |\n| **x** | `y` |\n\nfollowing")
	app.chat.appendChatItemWithIntent(message, chatAppendHydration)

	entry := app.chat.renderItem(message, 72)
	if entry.environment != app.renderEnvironment.identity() {
		t.Fatalf(
			"compat streaming item environment = %#v, want %#v",
			entry.environment,
			app.renderEnvironment.identity(),
		)
	}
	identity := message.streamingMd.fullCacheIdentity
	if identity.displayCellProfileID != profile.Identity() ||
		identity.themeGen != app.renderEnvironment.themeGen ||
		identity.geometryGen != app.renderEnvironment.geometryGen {
		t.Fatalf(
			"compat streaming Markdown identity = %#v, want %#v",
			identity,
			app.renderEnvironment.identity(),
		)
	}
}

func TestG11D1FinishedAndViewportCachesRequireExactGeometry(t *testing.T) {
	env := defaultRenderEnvironment(defaultStyles())
	chat := newChatViewWithRenderEnvironment(env)
	chat.AppendOrUpdateAssistant("| a | b |\n| --- | --- |\n| x | y |")
	chat.FinishAssistant()
	item := chat.items[0]

	first := chat.renderItem(item, 40)
	if first.environment != env.identity() {
		t.Fatalf("initial item cache identity = %#v, want %#v", first.environment, env.identity())
	}
	if sameWidth := chat.renderItem(item, 40); sameWidth != first {
		t.Fatal("exact width and environment did not reuse frozen cache")
	}
	if adjacentWidth := chat.renderItem(item, 41); adjacentWidth == first {
		t.Fatal("finished item reused cache across adjacent widths")
	}

	profiled := newRenderEnvironment(env.styles, g11D1Profile(8))
	chat.SetRenderEnvironment(profiled)
	if changedProfile := chat.renderItem(item, 41); changedProfile == first ||
		changedProfile.environment != profiled.identity() {
		t.Fatalf("profile change reused stale item cache: %#v", changedProfile)
	}

	chat.SetSize(80, 8)
	chat.Render(80, 8)
	if chat.viewCacheEnvironment != profiled.identity() {
		t.Fatalf(
			"viewport cache identity = %#v, want %#v",
			chat.viewCacheEnvironment,
			profiled.identity(),
		)
	}
	resized := profiled.withGeometry()
	chat.SetRenderEnvironment(resized)
	chat.Render(80, 8)
	if chat.viewCacheEnvironment != resized.identity() {
		t.Fatalf(
			"viewport cache retained old geometry: %#v",
			chat.viewCacheEnvironment,
		)
	}
}

func TestG11D1SelectedProfilePreservesMixedTableLifecycle(t *testing.T) {
	const table = "| `eino-agent` | **Codebase** | Mixed |\n" +
		"| --- | --- | --- |\n" +
		"| 🏷 | क्ष | plain **bold** `code` suffix |"
	env := newRenderEnvironment(defaultStyles(), g11D1Profile(8))
	stream := &StreamingMarkdown{}

	incomplete := stream.renderWithEnvironment(table, 72, env)
	if plain := xansi.Strip(incomplete); strings.Contains(plain, "┌") ||
		!strings.Contains(plain, "| `eino-agent` |") {
		t.Fatalf("incomplete table lifecycle changed: %q", plain)
	}
	source := table + "\n\nfollowing sibling"
	promoted := stream.renderWithEnvironment(source, 72, env)
	if plain := xansi.Strip(promoted); !strings.Contains(plain, "┌") {
		t.Fatalf("stable table was not promoted: %q", plain)
	}
	if stream.stableCacheIdentity.displayCellProfileID != env.profile.Identity() {
		t.Fatalf("stable table profile = %#v", stream.stableCacheIdentity)
	}

	stream.Finalize(source)
	finalized := stream.renderWithEnvironment(source, 72, env)
	plain := xansi.Strip(finalized)
	for _, want := range []string{
		"eino-agent", "Codebase", "plain", "bold", "code", "suffix",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("final mixed table lost %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "`eino-agent`") ||
		strings.Contains(plain, "**Codebase**") ||
		strings.Contains(plain, "**bold**") ||
		strings.Contains(plain, "`code`") {
		t.Fatalf("final mixed table exposed Markdown markers: %q", plain)
	}
	if !strings.Contains(finalized, "\x1b[") {
		t.Fatalf("final mixed table lost styled partial runs: %q", finalized)
	}
}
