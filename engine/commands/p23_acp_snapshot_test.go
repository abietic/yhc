package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const p233EmptyDigest = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"

func p233TestHandler(_ context.Context, _ *CommandContext) (*CommandResult, error) {
	return &CommandResult{}, nil
}

func p233MustRegister(t *testing.T, r *Registry, cmd *Command) {
	t.Helper()
	if cmd.Execute == nil {
		cmd.Execute = p233TestHandler
	}
	if err := r.Register(cmd); err != nil {
		t.Fatalf("register /%s: %v", cmd.Name, err)
	}
}

func p233DigestOf(t *testing.T, canonicalJSON string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(canonicalJSON))
	return hex.EncodeToString(sum[:])
}

func p233RowNames(snapshot CommandDiscoverySnapshot) []string {
	names := make([]string, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		names = append(names, row.Name)
	}
	return names
}

func TestP233CommandDiscoverySnapshotOrderedRowsAndCanonicalDigest(t *testing.T) {
	r := NewRegistry()
	p233MustRegister(t, r, &Command{
		Name:         "zeta",
		Description:  "last",
		Usage:        "/zeta",
		DisplayOrder: 30,
		Entrypoints:  EntrypointsACP,
	})
	p233MustRegister(t, r, &Command{
		Name:         "alpha",
		Description:  "first",
		Usage:        "/alpha <target>",
		DisplayOrder: 10,
		Entrypoints:  EntrypointsACP,
	})
	p233MustRegister(t, r, &Command{
		Name:         "mid",
		Description:  "middle",
		Usage:        "/mid",
		DisplayOrder: 20,
		Entrypoints:  EntrypointsACP,
	})

	snapshot := r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)

	got := p233RowNames(snapshot)
	want := []string{"alpha", "mid", "zeta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("row order = %v, want %v (ListForContext order)", got, want)
	}
	if snapshot.Rows[0].Input == nil || snapshot.Rows[0].Input.Hint != "<target>" {
		t.Fatalf("alpha input = %+v, want hint <target>", snapshot.Rows[0].Input)
	}
	if snapshot.Rows[1].Input != nil || snapshot.Rows[2].Input != nil {
		t.Fatalf(
			"no-argument rows must omit input, got %+v and %+v",
			snapshot.Rows[1].Input,
			snapshot.Rows[2].Input,
		)
	}

	canonical := `[{"name":"alpha","description":"first","input":{"hint":"<target>"}},` +
		`{"name":"mid","description":"middle"},` +
		`{"name":"zeta","description":"last"}]`
	if want := p233DigestOf(t, canonical); snapshot.Digest != want {
		t.Fatalf("digest = %q, want SHA-256 of canonical rows %q (%q)", snapshot.Digest, canonical, want)
	}
}

func TestP233CommandDiscoverySnapshotContextFiltering(t *testing.T) {
	r := NewRegistry()
	resolverCalls := 0
	p233MustRegister(t, r, &Command{
		Name:        "acp-only",
		Description: "visible on ACP",
		Usage:       "/acp-only",
		Entrypoints: EntrypointsACP,
		ResolveAvailability: func(context.Context, *CommandContext) (AvailabilityState, string) {
			resolverCalls++
			return AvailabilitySupported, ""
		},
	})
	p233MustRegister(t, r, &Command{
		Name:        "tui-only",
		Description: "visible on TUI",
		Usage:       "/tui-only",
		Entrypoints: EntrypointsTUI,
	})
	p233MustRegister(t, r, &Command{
		Name:        "capability-down",
		Description: "narrowed away by the runtime capability resolver",
		Usage:       "/capability-down",
		Entrypoints: EntrypointsACP,
		ResolveAvailability: func(context.Context, *CommandContext) (AvailabilityState, string) {
			return AvailabilityUnavailable, "capability is off"
		},
	})

	snapshot := r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	got := p233RowNames(snapshot)
	if len(got) != 1 || got[0] != "acp-only" {
		t.Fatalf("ACP rows = %v, want only [acp-only]", got)
	}
	if resolverCalls != 1 {
		t.Fatalf("supported ACP resolver calls = %d, want exactly 1 (single ListForContext pass)", resolverCalls)
	}

	tuiSnapshot := r.DiscoverySnapshotForContext(context.Background(), EntrypointTUI, nil)
	got = p233RowNames(tuiSnapshot)
	if len(got) != 1 || got[0] != "tui-only" {
		t.Fatalf("TUI rows = %v, want only [tui-only]", got)
	}
}

func TestP233CommandDiscoverySnapshotUsageSuffixHint(t *testing.T) {
	r := NewRegistry()
	p233MustRegister(t, r, &Command{
		Name:        "ctx",
		Description: "usage suffix hint",
		Usage:       "/ctx   status --verbose  ",
		Entrypoints: EntrypointsACP,
	})

	snapshot := r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	if len(snapshot.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(snapshot.Rows))
	}
	row := snapshot.Rows[0]
	if row.Input == nil || row.Input.Hint != "status --verbose" {
		t.Fatalf("hint = %+v, want trimmed usage suffix %q", row.Input, "status --verbose")
	}
}

func TestP233CommandDiscoverySnapshotArgDefFallback(t *testing.T) {
	r := NewRegistry()
	p233MustRegister(t, r, &Command{
		Name:        "cfg",
		Description: "no usage suffix, arg metadata supplies the hint",
		Usage:       "/cfg",
		Entrypoints: EntrypointsACP,
		Args: []ArgDef{
			{Name: "mode", Type: "string", Required: true},
			{Name: "level", Type: "string"},
			{Name: "format", Type: "string", Default: "json"},
		},
		DisplayOrder: 10,
	})
	p233MustRegister(t, r, &Command{
		Name:         "foreign",
		Description:  "usage without the canonical prefix falls back to ArgDef",
		Usage:        "configure <mode>",
		Entrypoints:  EntrypointsACP,
		Args:         []ArgDef{{Name: "mode", Type: "string", Required: true}},
		DisplayOrder: 20,
	})
	p233MustRegister(t, r, &Command{
		Name:         "glued",
		Description:  "prefix not separated by whitespace is not canonical",
		Usage:        "/glued<mode>",
		Entrypoints:  EntrypointsACP,
		Args:         []ArgDef{{Name: "mode", Type: "string", Required: true}},
		DisplayOrder: 30,
	})

	snapshot := r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	if len(snapshot.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(snapshot.Rows))
	}
	assertions := []struct {
		row  int
		hint string
	}{
		{0, "<mode> [level] [format=json]"},
		{1, "<mode>"},
		{2, "<mode>"},
	}
	for _, assertion := range assertions {
		row := snapshot.Rows[assertion.row]
		if row.Input == nil || row.Input.Hint != assertion.hint {
			t.Errorf(
				"row %d (%s) hint = %+v, want %q",
				assertion.row,
				row.Name,
				row.Input,
				assertion.hint,
			)
		}
	}
}

func TestP233CommandDiscoverySnapshotOmitsInputWithoutHint(t *testing.T) {
	r := NewRegistry()
	p233MustRegister(t, r, &Command{
		Name:        "plain",
		Description: "no usage suffix and no args",
		Usage:       "/plain",
		Entrypoints: EntrypointsACP,
	})

	snapshot := r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	if len(snapshot.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(snapshot.Rows))
	}
	row := snapshot.Rows[0]
	if row.Input != nil {
		t.Fatalf("input = %+v, want nil for a command without any hint", row.Input)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	if strings.Contains(string(encoded), "input") {
		t.Fatalf("omitted input must not appear in the canonical JSON, got %s", encoded)
	}
	if want := p233DigestOf(
		t,
		`[{"name":"plain","description":"no usage suffix and no args"}]`,
	); snapshot.Digest != want {
		t.Fatalf("digest = %q, want %q", snapshot.Digest, want)
	}
}

func TestP233CommandDiscoverySnapshotDigestTracksProjectedRowsOnly(t *testing.T) {
	build := func(
		detailedHelp string,
		displayOrder int,
		description string,
		usage string,
	) CommandDiscoverySnapshot {
		r := NewRegistry()
		p233MustRegister(t, r, &Command{
			Name:         "track",
			Description:  description,
			Usage:        usage,
			DetailedHelp: detailedHelp,
			DisplayOrder: displayOrder,
			Entrypoints:  EntrypointsACP,
		})
		return r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	}

	base := build("help v1", 10, "one", "/track")
	unprojected := build("completely different help", 20, "one", "/track")
	if base.Digest != unprojected.Digest {
		t.Fatalf("digest changed with unprojected metadata: %q vs %q", base.Digest, unprojected.Digest)
	}
	if len(unprojected.Rows) != 1 || unprojected.Rows[0].Description != "one" {
		t.Fatalf("unprojected rows = %+v, want the same projected row", unprojected.Rows)
	}

	changedDescription := build("help v1", 10, "two", "/track")
	if changedDescription.Digest == base.Digest {
		t.Fatal("digest must change when a projected description changes")
	}
	changedHint := build("help v1", 10, "one", "/track [flag]")
	if changedHint.Digest == base.Digest {
		t.Fatal("digest must change when a projected input hint appears")
	}
}

func TestP233CommandDiscoverySnapshotPromptGenerationIsNotIdentity(t *testing.T) {
	r := NewRegistry()
	p233MustRegister(t, r, &Command{
		Name:        "alpha",
		Description: "core command",
		Usage:       "/alpha",
		Entrypoints: EntrypointsTUI,
	})
	pluginCommand := func() *Command {
		return &Command{
			Name:        "plugcmd",
			Description: "configured prompt command",
			Usage:       "/plugcmd",
			Source:      string(CommandSourcePlugin),
			Trust:       CommandTrustConfigured,
			Entrypoints: EntrypointsTUI,
			Execute:     p233TestHandler,
		}
	}

	first, err := r.ReplacePromptCommandGeneration(PromptCommandGenerationCandidate{
		Digest:   "generation-digest-one",
		Commands: []*Command{pluginCommand()},
	})
	if err != nil {
		t.Fatalf("replace generation one: %v", err)
	}
	snapshotOne := r.DiscoverySnapshotForContext(context.Background(), EntrypointTUI, nil)

	second, err := r.ReplacePromptCommandGeneration(PromptCommandGenerationCandidate{
		Digest:   "generation-digest-two",
		Commands: []*Command{pluginCommand()},
	})
	if err != nil {
		t.Fatalf("replace generation two: %v", err)
	}
	snapshotTwo := r.DiscoverySnapshotForContext(context.Background(), EntrypointTUI, nil)

	if second.Revision == first.Revision || second.Digest == first.Digest {
		t.Fatalf("generation metadata must advance: first=%+v second=%+v", first, second)
	}
	if snapshotOne.Digest != snapshotTwo.Digest {
		t.Fatalf(
			"identical projected rows must keep one digest: %q vs %q",
			snapshotOne.Digest,
			snapshotTwo.Digest,
		)
	}
	if snapshotTwo.Digest == second.Digest || snapshotTwo.Digest == first.Digest {
		t.Fatalf("snapshot digest %q must never be the generation digest", snapshotTwo.Digest)
	}
	live := r.PromptCommandGeneration()
	if live.Digest != "generation-digest-two" {
		t.Fatalf("live generation digest = %q, want the committed candidate digest", live.Digest)
	}
}

func TestP233CommandDiscoverySnapshotEmptyAndNilRegistry(t *testing.T) {
	var nilRegistry *Registry
	nilSnapshot := nilRegistry.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	if len(nilSnapshot.Rows) != 0 {
		t.Fatalf("nil registry rows = %v, want none", nilSnapshot.Rows)
	}
	if nilSnapshot.Digest != p233EmptyDigest {
		t.Fatalf("nil registry digest = %q, want SHA-256 of [] (%q)", nilSnapshot.Digest, p233EmptyDigest)
	}

	emptySnapshot := NewRegistry().DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	if emptySnapshot.Rows == nil || len(emptySnapshot.Rows) != 0 {
		t.Fatalf("empty registry rows = %v, want a non-nil empty slice so it serializes as []", emptySnapshot.Rows)
	}
	if emptySnapshot.Digest != p233EmptyDigest {
		t.Fatalf("empty registry digest = %q, want %q", emptySnapshot.Digest, p233EmptyDigest)
	}
}

func TestP233CommandDiscoverySnapshotRowsAreIndependentAcrossCalls(t *testing.T) {
	r := NewRegistry()
	p233MustRegister(t, r, &Command{
		Name:        "alpha",
		Description: "first",
		Usage:       "/alpha <target>",
		Entrypoints: EntrypointsACP,
	})

	first := r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	second := r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	if first.Digest != second.Digest {
		t.Fatalf("stable rows must keep one digest: %q vs %q", first.Digest, second.Digest)
	}

	first.Rows[0].Name = "mutated"
	first.Rows[0].Input.Hint = "mutated"
	if second.Rows[0].Name != "alpha" || second.Rows[0].Input.Hint != "<target>" {
		t.Fatalf("mutating one snapshot leaked into another: %+v", second.Rows[0])
	}

	third := r.DiscoverySnapshotForContext(context.Background(), EntrypointACP, nil)
	if third.Rows[0].Name != "alpha" ||
		third.Rows[0].Input.Hint != "<target>" ||
		third.Digest != second.Digest {
		t.Fatalf(
			"later snapshots must be unaffected by caller mutation: %+v digest %q",
			third.Rows[0],
			third.Digest,
		)
	}
}
