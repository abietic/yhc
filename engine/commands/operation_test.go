package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/skills"
)

func TestOperationBuildInputPreservesArguments(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Command{Name: "form", Args: []ArgDef{{Name: "first", Required: true}, {Name: "count", Type: "int", Required: true}, {Name: "tail"}}, Execute: func(context.Context, *CommandContext) (*CommandResult, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	input, err := r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "form", []string{"中文 a", "2", `a'"\\b`})
	if err != nil {
		t.Fatal(err)
	}
	name, args, err := parseCommandInputStrict(input)
	if err != nil || name != "form" || !sameStrings(args, []string{"中文 a", "2", `a'"\\b`}) {
		t.Fatalf("round trip %q %#v %v", input, args, err)
	}
	if _, err := r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "form", []string{"x", "bad"}); err == nil {
		t.Fatal("invalid int accepted")
	}
	input, err = r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "form", []string{"x", "2", ""})
	if err != nil || strings.Contains(input, `""`) {
		t.Fatalf("trailing empty = %q %v", input, err)
	}
}

func TestOperationDescriptionAvailabilityAndToken(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Command{Name: "hidden", Availability: AvailabilityHidden, Execute: func(context.Context, *CommandContext) (*CommandResult, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&Command{Name: "blocked", Availability: AvailabilityUnavailable, AvailabilityReason: "blocked", Execute: func(context.Context, *CommandContext) (*CommandResult, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	for _, d := range r.DescribeOperations(context.Background(), EntrypointTUI, nil) {
		if d.Command.Name == "hidden" {
			t.Fatal("hidden operation exposed")
		}
	}
	d, ok := r.DescribeOperation(context.Background(), EntrypointTUI, nil, "blocked", nil)
	if !ok || d.Command.AvailabilityReason != "blocked" {
		t.Fatalf("availability %#v %v", d, ok)
	}
	s := skills.NewSkillRegistry()
	s.Register(&skills.Skill{Name: "same", Content: "one", DisableModelInvocation: true})
	r.SetSkillRegistry(s)
	first, ok := r.DescribeOperation(context.Background(), EntrypointTUI, nil, "skill:same", nil)
	if !ok {
		t.Fatal("skill missing")
	}
	if first.Command.SourcePath != "" || first.Command.ModelInvocation != ModelInvocationPrompt {
		t.Fatalf("skill policy %#v", first.Command)
	}
	s.Register(&skills.Skill{Name: "same", Content: "two", DisableModelInvocation: true})
	second, _ := r.DescribeOperation(context.Background(), EntrypointTUI, nil, "skill:same", nil)
	if first.Token == second.Token {
		t.Fatal("skill reload did not change token")
	}
}

func TestOperationBranchFieldsAndEmptyToken(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Command{Name: "branch", Completion: []ArgumentCompletion{{Choices: []string{"x"}, Branches: map[string][]ArgumentCompletion{"x": {{Source: "files"}}}}}, Execute: func(context.Context, *CommandContext) (*CommandResult, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	d, ok := r.DescribeOperation(context.Background(), EntrypointTUI, nil, "branch", []string{"x"})
	if !ok || len(d.Fields) != 2 || d.Fields[1].Name != "files" {
		t.Fatalf("fields %#v", d.Fields)
	}
	name, args, err := parseCommandInputStrict(`/branch a "" b`)
	if err != nil || name != "branch" || !strings.EqualFold(strings.Join(args, "|"), "a||b") {
		t.Fatalf("empty token %#v %v", args, err)
	}
}

func TestOperationFormSchemaAndValidation(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Command{Name: "typed", Args: []ArgDef{{Name: "optional"}, {Name: "required", Required: true}, {Name: "count", Type: "int"}, {Name: "enabled", Type: "bool"}}, Execute: func(context.Context, *CommandContext) (*CommandResult, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	input, err := r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "typed", []string{"", "ok", "", "yes"})
	if err != nil {
		t.Fatal(err)
	}
	_, args := ParseCommandInput(input)
	if !sameStrings(args, []string{"", "ok", "", "yes"}) {
		t.Fatalf("positional values = %#v", args)
	}
	for _, values := range [][]string{{"", ""}, {"x", "ok", "-"}, {"x", "ok", "1-2"}, {"x", "ok", "2", "maybe"}, {"bad\x1b[2J", "ok"}, {"bad\u0085", "ok"}, {"x\ny", "ok"}} {
		if _, err := r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "typed", values); err == nil {
			t.Fatalf("invalid values accepted: %#v", values)
		}
	}
	s := skills.NewSkillRegistry()
	s.Register(&skills.Skill{Name: "typed", Source: "project", FilePath: ".agents/skills/typed/SKILL.md", Content: "{{target}} {{mode}}", Args: []skills.SkillArg{{Name: "target", Required: true}, {Name: "mode", Default: "safe", Choices: []string{"safe", "fast"}}}})
	r.SetSkillRegistry(s)
	d, ok := r.DescribeOperation(context.Background(), EntrypointTUI, nil, "skill:typed", nil)
	if !ok || len(d.Fields) != 2 || !d.Fields[0].Required || d.Fields[1].Default != "safe" || d.Command.SourcePath != ".agents/skills/typed/SKILL.md" {
		t.Fatalf("Skill schema = %#v", d)
	}
	input, err = r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "skill:typed", []string{"pkg"})
	if err != nil || input != "/skill:typed pkg safe" {
		t.Fatalf("default = %q %v", input, err)
	}
	// Choices are suggestions; manually invoking an extension still accepts other text.
	if _, err := r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "skill:typed", []string{"pkg", "custom"}); err != nil {
		t.Fatal(err)
	}
	d.Fields[1].Choices[0] = "mutated"
	next, _ := r.DescribeOperation(context.Background(), EntrypointTUI, nil, "skill:typed", nil)
	if next.Fields[1].Choices[0] != "safe" {
		t.Fatal("description mutated registry")
	}
}

func TestOperationCatalogNeverExecutesAndOmitsRuntimeHidden(t *testing.T) {
	r := NewRegistry()
	called := 0
	if err := r.Register(&Command{Name: "catalog", Aliases: []string{"alias"}, ResolveAvailability: func(context.Context, *CommandContext) (AvailabilityState, string) {
		return AvailabilityHidden, "private"
	}, Execute: func(context.Context, *CommandContext) (*CommandResult, error) { called++; return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	for _, d := range r.DescribeOperations(context.Background(), EntrypointTUI, nil) {
		if d.Command.Name == "catalog" {
			t.Fatal("runtime-hidden command exposed")
		}
	}
	if _, ok := r.DescribeOperation(context.Background(), EntrypointTUI, nil, "alias", nil); ok {
		t.Fatal("runtime-hidden alias exposed")
	}
	if _, err := r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "alias", nil); err == nil {
		t.Fatal("hidden command built")
	}
	if called != 0 {
		t.Fatal("metadata lookup executed command")
	}
	RegisterDefaults(r)
	for name, want := range map[string]ModelInvocationKind{"help": ModelInvocationNone, "compact": ModelInvocationPossible, "goal": ModelInvocationPossible} {
		d, ok := r.DescribeOperation(context.Background(), EntrypointTUI, nil, name, nil)
		if !ok || d.Command.ModelInvocation != want {
			t.Fatalf("%s model policy = %#v", name, d.Command)
		}
	}
}

func TestOperationAvailabilityUsesEncodedArguments(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r)
	active := &CommandContext{Environment: CommandEnvironment{Entrypoint: EntrypointTUI, Phase: CommandPhaseActiveTurn}}
	input, err := r.BuildOperationInput(context.Background(), EntrypointTUI, active, "agent", []string{""})
	if err != nil || input != "/agent" {
		t.Fatalf("empty active agent form = %q %v", input, err)
	}
	d, ok := r.DescribeOperation(context.Background(), EntrypointTUI, active, "agent", []string{""})
	if !ok || d.Command.Availability != AvailabilitySupported {
		t.Fatalf("empty active agent availability = %#v", d.Command)
	}
	if _, err := r.BuildOperationInput(context.Background(), EntrypointTUI, active, "agent", []string{"create"}); err == nil {
		t.Fatal("active definition-management accepted")
	}
	if err := r.Register(&Command{Name: "rest-admission", Completion: []ArgumentCompletion{{Rest: true}}, ResolveAvailability: func(_ context.Context, ctx *CommandContext) (AvailabilityState, string) {
		if len(ctx.Args) != 2 {
			return AvailabilityUnavailable, "two arguments needed"
		}
		return AvailabilitySupported, ""
	}, Execute: func(context.Context, *CommandContext) (*CommandResult, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	input, err = r.BuildOperationInput(context.Background(), EntrypointTUI, nil, "rest-admission", []string{`one "two words"`})
	if err != nil || input != `/rest-admission one "two words"` {
		t.Fatalf("rest admission = %q %v", input, err)
	}
}
