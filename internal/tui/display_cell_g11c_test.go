package tui

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestG11CDefaultProfileIdentityCoversEveryPolicyField(t *testing.T) {
	basePolicy := defaultDisplayCellPolicy()
	base := newDisplayCellProfile(basePolicy)
	if !base.valid() {
		t.Fatal("default display-cell profile is invalid")
	}
	if base.Identity() == "" ||
		!strings.HasPrefix(base.Identity(), displayCellIdentityVersion+"/sha256:") {
		t.Fatalf("profile identity = %q, want derived versioned SHA-256", base.Identity())
	}
	if got := newDisplayCellProfile(basePolicy).Identity(); got != base.Identity() {
		t.Fatalf("identical policy identity = %q, want %q", got, base.Identity())
	}

	tests := []struct {
		name   string
		mutate func(*displayCellPolicy)
	}{
		{"unicode", func(p *displayCellPolicy) { p.UnicodeVersion = "17.0.1" }},
		{"segmentation", func(p *displayCellPolicy) { p.SegmentationMethod += "-other" }},
		{"width-method", func(p *displayCellPolicy) { p.WidthMethod += "-other" }},
		{"ambiguous", func(p *displayCellPolicy) { p.AmbiguousWidth = "wide" }},
		{"emoji", func(p *displayCellPolicy) { p.EmojiPresentation += "-other" }},
		{"tab-stop", func(p *displayCellPolicy) { p.TabStop++ }},
		{"ansi-7", func(p *displayCellPolicy) { p.ControlSequences7Bit = false }},
		{"ansi-8", func(p *displayCellPolicy) { p.ControlSequences8Bit = true }},
		{"indic", func(p *displayCellPolicy) { p.IndicConjunctCells++ }},
		{"lone-ri", func(p *displayCellPolicy) { p.LoneRegionalIndicatorCell++ }},
		{"paired-flag", func(p *displayCellPolicy) { p.PairedFlagCells++ }},
		{"bare-label", func(p *displayCellPolicy) { p.BareLabelCells++ }},
		{"unsafe-controls", func(p *displayCellPolicy) { p.UnsafeControlPolicy += "-other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := basePolicy
			test.mutate(&policy)
			changed := newDisplayCellProfile(policy)
			if !changed.valid() {
				t.Fatalf("mutated policy produced invalid profile: %+v", policy)
			}
			if changed.Identity() == base.Identity() {
				t.Fatalf("field %s did not change derived identity", test.name)
			}
		})
	}
}

func TestG11CProfileMeasuresTabsFromOwningRectangleOrigin(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	tests := []struct {
		origin int
		want   int
	}{
		{origin: 0, want: 5},
		{origin: 1, want: 4},
		{origin: 2, want: 3},
		{origin: 3, want: 6},
		{origin: 4, want: 5},
	}
	for _, test := range tests {
		if got := profile.measure("a\tb", test.origin); got != test.want {
			t.Errorf("origin=%d measure=%d, want %d", test.origin, got, test.want)
		}
	}

	clusters := profile.clusters("a\tb", 2)
	if len(clusters) != 3 {
		t.Fatalf("clusters=%d, want 3: %#v", len(clusters), clusters)
	}
	tab := clusters[1]
	if tab.source != "\t" || tab.text != " " || tab.cells != 1 ||
		tab.startColumn != 3 || tab.endColumn != 4 {
		t.Fatalf("tab cluster = %#v", tab)
	}

	wrapped := profile.wrapAt("a\tb", 4, true, 0)
	if got := strings.Join(wrapped, ""); got != "a   b" {
		t.Fatalf("wrapped tab projection = %q, want expanded bytes", got)
	}
	for _, line := range wrapped {
		if strings.Contains(line, "\t") {
			t.Fatalf("wrapped line retained cursor-moving tab: %q", line)
		}
		if got := profile.measure(line, 0); got > 4 {
			t.Fatalf("wrapped line width=%d, want <=4: %q", got, line)
		}
	}
	if got := profile.truncateAt("a\tb", 4, 0); got != "a   " {
		t.Fatalf("truncated tab projection = %q, want %q", got, "a   ")
	}
	if got := profile.padAligned("A", 5, "right", 2); got != "    A" {
		t.Fatalf("right padding = %q, want %q", got, "    A")
	}
	if got := profile.padAligned("A", 5, "center", 2); got != "  A  " {
		t.Fatalf("center padding = %q, want %q", got, "  A  ")
	}
}

func TestG11CCrossRunClustersUseFirstVisibleScalarPresentation(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	base := displayCellPresentation{italic: true, linkURL: "https://base.test"}
	tests := []struct {
		name       string
		runs       []displayCellRun
		wantSource string
		wantCells  int
	}{
		{
			name: "combining-bold",
			runs: []displayCellRun{
				{text: "e", presentation: base},
				{text: "\u0301", presentation: displayCellPresentation{bold: true}},
			},
			wantSource: "e\u0301",
			wantCells:  1,
		},
		{
			name: "combining-code",
			runs: []displayCellRun{
				{text: "e", presentation: base},
				{text: "\u0301", presentation: displayCellPresentation{code: true}},
			},
			wantSource: "e\u0301",
			wantCells:  1,
		},
		{
			name: "combining-link",
			runs: []displayCellRun{
				{text: "e", presentation: base},
				{text: "\u0301", presentation: displayCellPresentation{linkURL: "https://later.test"}},
			},
			wantSource: "e\u0301",
			wantCells:  1,
		},
		{
			name: "combining-strike",
			runs: []displayCellRun{
				{text: "e", presentation: base},
				{text: "\u0301", presentation: displayCellPresentation{strike: true}},
			},
			wantSource: "e\u0301",
			wantCells:  1,
		},
		{
			name: "combining-image",
			runs: []displayCellRun{
				{text: "e", presentation: base},
				{text: "\u0301", presentation: displayCellPresentation{image: true}},
			},
			wantSource: "e\u0301",
			wantCells:  1,
		},
		{
			name: "zwj-extension",
			runs: []displayCellRun{
				{text: "👩🏽", presentation: base},
				{text: "\u200d💻", presentation: displayCellPresentation{code: true}},
			},
			wantSource: "👩🏽\u200d💻",
			wantCells:  2,
		},
		{
			name: "variation-extension",
			runs: []displayCellRun{
				{text: "❤", presentation: base},
				{text: "\ufe0f", presentation: displayCellPresentation{linkURL: "https://later.test"}},
			},
			wantSource: "❤️",
			wantCells:  2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clusters := profile.measureRuns(test.runs, 7)
			if len(clusters) != 1 {
				t.Fatalf("clusters=%d, want 1: %#v", len(clusters), clusters)
			}
			cluster := clusters[0]
			if cluster.source != test.wantSource || cluster.text != test.wantSource ||
				cluster.cells != test.wantCells {
				t.Fatalf("measured cluster = %#v", cluster)
			}
			if cluster.presentation != base {
				t.Fatalf(
					"presentation = %#v, want first-visible owner %#v",
					cluster.presentation,
					base,
				)
			}
			if strings.Contains(cluster.text, "\x1b") {
				t.Fatalf("measured cluster contains internal terminal control: %q", cluster.text)
			}
		})
	}
}

func TestG11CUnsafeControlsKeepSourceAndSanitizeTerminalProjection(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	input := "a\x00\r\u202eb"
	clusters := profile.clusters(input, 0)
	var source, projected strings.Builder
	for _, cluster := range clusters {
		source.WriteString(cluster.source)
		projected.WriteString(cluster.text)
	}
	if source.String() != input {
		t.Fatalf("cluster source = %q, want canonical bytes %q", source.String(), input)
	}
	if got, want := projected.String(), "a\uFFFD\uFFFD\uFFFDb"; got != want {
		t.Fatalf("terminal projection = %q, want %q", got, want)
	}
	if got := profile.measure(input, 0); got != 5 {
		t.Fatalf("sanitized control width = %d, want 5", got)
	}

	prependControl := "\u061cب"
	projected.Reset()
	for _, cluster := range profile.clusters(prependControl, 0) {
		projected.WriteString(cluster.text)
	}
	if got, want := projected.String(), "\uFFFDب"; got != want {
		t.Fatalf("prepend-control projection = %q, want %q", got, want)
	}
	if got := profile.measure(prependControl, 0); got != 2 {
		t.Fatalf("prepend-control width = %d, want 2", got)
	}

	invalidUTF8 := string([]byte{'a', 0xff, 'b'})
	projected.Reset()
	for _, cluster := range profile.clusters(invalidUTF8, 0) {
		projected.WriteString(cluster.text)
	}
	if got := projected.String(); got != "a\uFFFDb" {
		t.Fatalf("invalid UTF-8 projection = %q, want replacement", got)
	}

	ansi := profile.clusters("\x1b[31mA\x1b[0m", 0)
	if len(ansi) != 3 || !ansi[0].control || ansi[1].text != "A" ||
		!ansi[2].control {
		t.Fatalf("supported ANSI classification = %#v", ansi)
	}
}

func TestG11CClusterOperationsPreserveProgressControlsAndBounds(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	inputs := []string{
		"ASCII and 中",
		"e\u0301 क्ष ❤️ 👩🏽\u200d💻 🇺 🇺🇸 1️⃣",
		"\x1b[31mabcdef\x1b[0m",
		"\x1b]8;;https://example.test\x1b\\abcdef\x1b]8;;\x1b\\",
		"a\tb",
		"a\x00\u202eb",
	}
	for _, input := range inputs {
		for _, origin := range []int{0, 2, 7} {
			expanded := profile.expandTabs(input, origin)
			for width := 1; width <= 8; width++ {
				lines := profile.wrapAt(input, width, true, origin)
				if len(lines) == 0 {
					t.Fatalf("input=%q origin=%d width=%d made no progress", input, origin, width)
				}
				if got := xansi.Strip(strings.Join(lines, "")); got != xansi.Strip(expanded) {
					t.Fatalf(
						"input=%q origin=%d width=%d changed projection: got %q want %q",
						input,
						origin,
						width,
						got,
						xansi.Strip(expanded),
					)
				}
				for _, line := range lines {
					assertWidthProfileControlStateClosed(t, profile, line)
					if got := profile.measure(line, origin); got > width &&
						visibleClusterCount(profile, line) != 1 {
						t.Fatalf(
							"input=%q origin=%d line width=%d exceeds %d: %q",
							input,
							origin,
							got,
							width,
							line,
						)
					}
				}
				truncated := profile.truncateAt(input, width, origin)
				assertWidthProfileControlStateClosed(t, profile, truncated)
				if got := profile.measure(truncated, origin); got > width {
					t.Fatalf(
						"input=%q origin=%d truncate width=%d exceeds %d",
						input,
						origin,
						got,
						width,
					)
				}
			}
		}
	}
}

func TestG11CAppCopiesInjectedProfileAndFallsBackFromZero(t *testing.T) {
	policy := defaultDisplayCellPolicy()
	policy.TabStop = 8
	injected := newDisplayCellProfile(policy)
	app := New(Config{Resumed: true, DisplayCellProfile: &injected})
	if app.renderEnvironment.profile.Identity() != injected.Identity() ||
		app.renderEnvironment.profile.policy.TabStop != 8 {
		t.Fatalf("App profile = %#v, want injected copy %#v", app.renderEnvironment.profile, injected)
	}

	injected.policy.TabStop = 16
	if app.renderEnvironment.profile.policy.TabStop != 8 {
		t.Fatalf("App profile changed after caller mutation: %#v", app.renderEnvironment.profile)
	}

	zero := DisplayCellProfile{}
	fallback := New(Config{Resumed: true, DisplayCellProfile: &zero})
	if got, want := fallback.renderEnvironment.profile.Identity(), DefaultDisplayCellProfile().Identity(); got != want {
		t.Fatalf("zero-profile fallback identity = %q, want %q", got, want)
	}
}
