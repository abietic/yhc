package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	MaxOperationFormFields = 32
	MaxOperationFormBytes  = 64 << 10
)

// ModelInvocationKind describes the command's model path, not its permissions.
type ModelInvocationKind string

const (
	ModelInvocationNone     ModelInvocationKind = "none"
	ModelInvocationPossible ModelInvocationKind = "possible"
	ModelInvocationPrompt   ModelInvocationKind = "prompt"
	ModelInvocationUnknown  ModelInvocationKind = "unknown"
)

// OperationField describes one form value. Rest values use slash argument
// quoting; other fields occupy one argv position. Choices are suggestions.
type OperationField struct {
	Name, Type, Description, Default, Source string
	Required, Rest                           bool
	Choices                                  []string
}

// OperationDescription is a detached projection of one in-memory definition.
// Token changes on source/schema reload; effective availability remains live.
type OperationDescription struct {
	Command *Command
	Fields  []OperationField
	Token   string
}

func applyOperationDefaults(cmd *Command) error {
	if cmd.ModelInvocation == "" {
		switch {
		case cmd.Kind == CommandKindPromptWorkflow || cmd.ResultKind == ResultKindPrompt:
			cmd.ModelInvocation = ModelInvocationPrompt
		case cmd.Name == "compact" || cmd.Name == "goal":
			cmd.ModelInvocation = ModelInvocationPossible
		case cmd.Source == string(CommandSourceCore) && cmd.Trust == CommandTrustCore && coreDiscoveryMetadata[cmd.Name].order != 0:
			cmd.ModelInvocation = ModelInvocationNone
		default:
			cmd.ModelInvocation = ModelInvocationUnknown
		}
	}
	switch cmd.ModelInvocation {
	case ModelInvocationNone, ModelInvocationPossible, ModelInvocationPrompt, ModelInvocationUnknown:
		return nil
	default:
		return fmt.Errorf("command %q has invalid model invocation %q", cmd.Name, cmd.ModelInvocation)
	}
}

// DescribeOperations includes explained unavailable commands, excluding hidden
// commands and unsupported entrypoints. It never discovers files or executes.
func (r *Registry) DescribeOperations(ctx context.Context, entrypoint Entrypoint, cmdCtx *CommandContext) []OperationDescription {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot := r.commandSnapshot()
	out := make([]OperationDescription, 0, len(snapshot.order))
	for _, name := range snapshot.order {
		cmd := snapshot.commands[name]
		if cmd == nil || cmd.Availability == AvailabilityHidden || !cmd.Entrypoints.Supports(entrypoint) {
			continue
		}
		copy := cloneCommand(cmd)
		state, reason := resolveCommandAvailability(ctx, copy, commandContextForEnvironment(cmdCtx, entrypoint))
		if state == AvailabilityHidden {
			continue
		}
		copy.Availability, copy.AvailabilityReason = state, reason
		out = append(out, describeOperation(copy, nil, snapshot.generation))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Command.DisplayOrder != out[j].Command.DisplayOrder {
			return out[i].Command.DisplayOrder < out[j].Command.DisplayOrder
		}
		return out[i].Command.Name < out[j].Command.Name
	})
	return out
}

// DescribeOperation resolves a name or alias with prospective form values.
func (r *Registry) DescribeOperation(ctx context.Context, entrypoint Entrypoint, cmdCtx *CommandContext, name string, values []string) (OperationDescription, bool) {
	if r == nil {
		return OperationDescription{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot := r.commandSnapshot()
	cmd := snapshot.commands[normalizeCommandKey(name)]
	if cmd == nil || cmd.Availability == AvailabilityHidden || !cmd.Entrypoints.Supports(entrypoint) {
		return OperationDescription{}, false
	}
	copy := cloneCommand(cmd)
	clonedCtx := commandContextForEnvironment(cmdCtx, entrypoint)
	clonedCtx.Args = append([]string(nil), values...)
	if values != nil {
		if args, err := operationArguments(describeOperation(copy, values, snapshot.generation), values); err == nil {
			clonedCtx.Args = args
		}
	}
	state, reason := resolveCommandAvailability(ctx, copy, clonedCtx)
	if state == AvailabilityHidden {
		return OperationDescription{}, false
	}
	copy.Availability, copy.AvailabilityReason = state, reason
	return describeOperation(copy, values, snapshot.generation), true
}

func describeOperation(cmd *Command, values []string, generation uint64) OperationDescription {
	fields := operationFields(cmd, values)
	return OperationDescription{Command: cloneCommand(cmd), Fields: fields, Token: operationToken(cmd, generation)}
}

func operationFields(cmd *Command, values []string) []OperationField {
	argDefs := cmd.Args
	if len(cmd.operationArgs) > 0 {
		argDefs = cmd.operationArgs
	}
	completion := append([]ArgumentCompletion(nil), cmd.Completion...)
	if len(completion) > 0 && len(values) > 0 && completion[0].Branches != nil {
		if branch, ok := completion[0].Branches[values[0]]; ok {
			completion = append(completion[:1:1], branch...)
		}
	}
	n := len(argDefs)
	if len(completion) > n {
		n = len(completion)
	}
	if n == 0 && cmd.ArgumentHint() != "" {
		return []OperationField{{Name: "arguments", Description: "command arguments", Rest: true}}
	}
	if n > MaxOperationFormFields {
		n = MaxOperationFormFields
	}
	fields := make([]OperationField, n)
	for i := 0; i < n; i++ {
		field := OperationField{Name: "arguments", Type: "string"}
		if i < len(argDefs) {
			a := argDefs[i]
			field.Name, field.Type, field.Description, field.Default, field.Required = a.Name, a.Type, a.Description, a.Default, a.Required
		}
		if i < len(completion) {
			c := completion[i]
			field.Choices = append([]string(nil), c.Choices...)
			if c.Default != "" {
				field.Default = c.Default
			}
			field.Source, field.Rest = c.Source, c.Rest
			if field.Name == "arguments" && c.Source != "" {
				field.Name = c.Source
			}
		}
		if field.Name == "" {
			field.Name = "value"
		}
		fields[i] = field
	}
	return fields
}

func operationToken(cmd *Command, generation uint64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", generation, cmd.Name, cmd.Source, cmd.SourcePath, cmd.Usage, cmd.ModelInvocation, cmd.SideEffect, cmd.definitionToken)
	fmt.Fprintf(&b, "\x00%#v\x00%#v\x00%#v", cmd.Args, cmd.operationArgs, cmd.Completion)
	s := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(s[:])
}

// BuildOperationInput validates and encodes values without dispatching. Final
// availability uses the actual argv, from the same static definition snapshot.
func (r *Registry) BuildOperationInput(ctx context.Context, entrypoint Entrypoint, cmdCtx *CommandContext, name string, values []string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return "", fmt.Errorf("command registry is nil")
	}
	snapshot := r.commandSnapshot()
	cmd := snapshot.commands[normalizeCommandKey(name)]
	if cmd == nil || cmd.Availability == AvailabilityHidden || !cmd.Entrypoints.Supports(entrypoint) {
		return "", fmt.Errorf("unknown or hidden command: /%s", normalizeCommandKey(name))
	}
	desc := describeOperation(cloneCommand(cmd), values, snapshot.generation)
	args, err := operationArguments(desc, values)
	if err != nil {
		return "", err
	}
	finalCtx := commandContextForEnvironment(cmdCtx, entrypoint)
	finalCtx.Args = args
	if state, reason := resolveCommandAvailability(ctx, desc.Command, finalCtx); state != AvailabilitySupported {
		return "", fmt.Errorf("/%s is unavailable: %s", desc.Command.Name, reason)
	}
	input := "/" + desc.Command.Name
	for _, arg := range args {
		input += " " + operationQuote(arg)
	}
	if len(input) > MaxOperationFormBytes {
		return "", fmt.Errorf("operation input exceeds limit")
	}
	parsedName, parsedArgs, err := parseCommandInputStrict(input)
	if err != nil || parsedName != desc.Command.Name || !sameStrings(parsedArgs, args) {
		return "", fmt.Errorf("operation input encoding failed")
	}
	return input, nil
}

func operationArguments(desc OperationDescription, values []string) ([]string, error) {
	if len(desc.Command.Completion) > 0 && len(values) > 0 && len(desc.Command.Completion[0].Branches[values[0]])+1 > MaxOperationFormFields {
		return nil, fmt.Errorf("command branch has too many fields for the form; use the composer")
	}
	if max(len(desc.Command.Args), len(desc.Command.operationArgs), len(desc.Command.Completion)) > MaxOperationFormFields {
		return nil, fmt.Errorf("command has too many fields for the form; use the composer")
	}
	if len(values) > MaxOperationFormFields || len(values) > len(desc.Fields) {
		return nil, fmt.Errorf("too many operation fields")
	}
	args := make([]string, 0, len(values))
	total := 0
	for i, field := range desc.Fields {
		value := ""
		if i < len(values) {
			value = values[i]
		}
		if value == "" && field.Default != "" {
			value = field.Default
		}
		total += len(value)
		if total > MaxOperationFormBytes || invalidOperationValue(value) {
			return nil, fmt.Errorf("invalid value for %s", field.Name)
		}
		if err := validateOperationField(field, value); err != nil {
			return nil, err
		}
		if field.Rest && i >= max(len(desc.Command.Args), len(desc.Command.operationArgs)) {
			if value == "" {
				continue
			}
			_, tail, err := parseCommandInputStrict("/operation " + value)
			if err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			args = append(args, tail...)
			continue
		}
		args = append(args, value)
	}
	for len(args) > 0 && args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}
	if err := desc.Command.ValidateArgs(args); err != nil {
		return nil, err
	}
	return args, nil
}

func invalidOperationValue(v string) bool {
	return strings.IndexFunc(v, unicode.IsControl) >= 0
}

func validateOperationField(f OperationField, v string) error {
	if v == "" && f.Required {
		return fmt.Errorf("%s is required", f.Name)
	}
	if v == "" {
		return nil
	}
	switch f.Type {
	case "", "string":
		return nil
	case "int":
		if _, err := strconv.Atoi(v); err != nil {
			return fmt.Errorf("%s must be an integer", f.Name)
		}
	case "bool":
		if err := validateArgType(v, ArgDef{Type: "bool"}); err != nil {
			return fmt.Errorf("%s must be a boolean", f.Name)
		}
	default:
		return fmt.Errorf("%s has unsupported type %s", f.Name, f.Type)
	}
	return nil
}

func operationQuote(v string) string {
	if v == "" || strings.ContainsAny(v, "'\\\"") || strings.IndexFunc(v, unicode.IsSpace) >= 0 {
		return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(v) + "\""
	}
	return v
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
