package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

var (
	deepPlatformPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	errDeepIntakeExists = errors.New("deep intake already exists")
)

var deepTargetOrder = []string{
	"check-boundaries",
	"test-fault-injection",
	"test-fuzz-deep",
	"test-e2e-deep",
	"test-pty-deep",
}

type DeepIntake struct {
	SchemaVersion    int        `json:"schema_version"`
	DiffDigest       string     `json:"diff_digest"`
	Target           string     `json:"target"`
	Status           GateStatus `json:"status"`
	Platform         string     `json:"platform"`
	FailureLogPath   string     `json:"failure_log_path,omitempty"`
	FirstFailingSeed string     `json:"first_failing_seed,omitempty"`
}

type DeepReport struct {
	SchemaVersion int            `json:"schema_version"`
	DiffDigest    string         `json:"diff_digest"`
	Status        GateStatus     `json:"status"`
	Platform      string         `json:"platform"`
	Gates         []GateEvidence `json:"gates"`
}

type DeepResult struct {
	Report DeepReport  `json:"report"`
	Intake *DeepIntake `json:"intake,omitempty"`
}

type DeepIntakeStore interface {
	Load(digest string) (DeepIntake, bool, error)
	Write(DeepIntake) error
}

type fileDeepIntakeStore struct {
	root string
}

func newFileDeepIntakeStore(root string) *fileDeepIntakeStore {
	return &fileDeepIntakeStore{root: root}
}

func selectedDeepTargets(plan Plan, policy Policy) ([]string, error) {
	if len(plan.Changed) == 0 || documentationOnly(plan) {
		return []string{}, nil
	}
	selected := map[string]struct{}{"check-boundaries": {}}
	for _, risk := range plan.Risks {
		pack, ok := policy.RiskPacks[risk]
		if !ok {
			return nil, fmt.Errorf("risk %q has no policy pack", risk)
		}
		for _, target := range pack.DeepTargets {
			if !slices.Contains(deepTargetOrder, target) || target == "check-boundaries" {
				return nil, fmt.Errorf("risk %q selects unsupported deep target %q", risk, target)
			}
			selected[target] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(selected))
	for _, target := range deepTargetOrder {
		if _, ok := selected[target]; ok {
			ordered = append(ordered, target)
		}
	}
	return ordered, nil
}

func runDeep(
	ctx context.Context,
	root string,
	plan Plan,
	policy Policy,
	platform string,
	runner TargetRunner,
	store DeepIntakeStore,
) (DeepResult, error) {
	if runner == nil || store == nil {
		return DeepResult{}, errors.New("deep verification dependencies are unavailable")
	}
	if !digestPattern.MatchString(plan.DiffDigest) || !deepPlatformPattern.MatchString(platform) {
		return DeepResult{}, errors.New("deep verification identity is invalid")
	}
	targets, err := selectedDeepTargets(plan, policy)
	if err != nil {
		return DeepResult{}, err
	}
	report := DeepReport{
		SchemaVersion: 1,
		DiffDigest:    plan.DiffDigest,
		Status:        GatePass,
		Platform:      platform,
		Gates:         []GateEvidence{},
	}
	if len(targets) == 0 {
		return DeepResult{Report: report}, nil
	}
	if existing, found, err := store.Load(plan.DiffDigest); err != nil {
		return DeepResult{}, err
	} else if found {
		return deepResultFromIntake(targets, existing)
	}

	for _, target := range targets {
		if target == "test-pty-deep" && platform == "windows" {
			report.Gates = append(report.Gates, GateEvidence{
				Target: target,
				Level:  "deep",
				Status: GateNotApplicable,
			})
			continue
		}
		result := runner.Run(ctx, root, plan.DiffDigest, target)
		gate, err := deepGateFromRunResult(plan.DiffDigest, target, result)
		if err != nil {
			return DeepResult{}, err
		}
		report.Gates = append(report.Gates, gate)
		if gate.Status != GateFail && gate.Status != GateBlocked {
			continue
		}
		intake := DeepIntake{
			SchemaVersion:    1,
			DiffDigest:       plan.DiffDigest,
			Target:           target,
			Status:           gate.Status,
			Platform:         platform,
			FailureLogPath:   gate.FailureLogPath,
			FirstFailingSeed: gate.FirstFailingSeed,
		}
		if err := store.Write(intake); err != nil {
			if !errors.Is(err, errDeepIntakeExists) {
				return DeepResult{}, err
			}
			winner, found, loadErr := store.Load(plan.DiffDigest)
			if loadErr != nil {
				return DeepResult{}, loadErr
			}
			if !found {
				return DeepResult{}, errors.New("concurrent deep intake disappeared")
			}
			return deepResultFromIntake(targets, winner)
		}
		return deepResultFromIntake(targets, intake)
	}
	return DeepResult{Report: report}, nil
}

func deepResultFromIntake(targets []string, intake DeepIntake) (DeepResult, error) {
	if err := validateDeepIntake(intake); err != nil {
		return DeepResult{}, err
	}
	report := DeepReport{
		SchemaVersion: 1,
		DiffDigest:    intake.DiffDigest,
		Status:        intake.Status,
		Platform:      intake.Platform,
		Gates:         make([]GateEvidence, 0, len(targets)),
	}
	for _, target := range targets {
		if target == intake.Target {
			if target == "test-pty-deep" && intake.Platform == "windows" {
				return DeepResult{}, errors.New("windows PTY intake cannot be terminal")
			}
			report.Gates = append(report.Gates, deepGateFromIntake(intake))
			return DeepResult{Report: report, Intake: &intake}, nil
		}
		status := GatePass
		if target == "test-pty-deep" && intake.Platform == "windows" {
			status = GateNotApplicable
		}
		report.Gates = append(report.Gates, GateEvidence{
			Target: target,
			Level:  "deep",
			Status: status,
		})
	}
	return DeepResult{}, errors.New("deep intake target is not selected by its plan")
}

func deepGateFromRunResult(digest, target string, result RunResult) (GateEvidence, error) {
	gate := GateEvidence{
		Target:           target,
		Level:            "deep",
		Status:           result.Status,
		ExitCode:         result.ExitCode,
		DurationMillis:   result.DurationMillis,
		FailureLogPath:   result.FailureLogPath,
		FirstFailingSeed: result.FirstFailingSeed,
	}
	if !digestPattern.MatchString(digest) || !slices.Contains(deepTargetOrder, target) || target == "" ||
		!oneOf(string(gate.Status), string(GatePass), string(GateFail), string(GateBlocked)) ||
		gate.DurationMillis < 0 {
		return GateEvidence{}, errors.New("invalid deep gate result")
	}
	if gate.FirstFailingSeed != "" &&
		(gate.Status != GateFail || !failureSeedPattern.MatchString(gate.FirstFailingSeed)) {
		return GateEvidence{}, errors.New("invalid deep failing seed")
	}
	if gate.FailureLogPath != "" {
		if gate.Status != GateFail && gate.Status != GateBlocked {
			return GateEvidence{}, errors.New("invalid deep failure log status")
		}
		want := path.Join(
			"build",
			"iteration",
			digest,
			"logs",
			safeTargetLogName(target)+".log",
		)
		if gate.FailureLogPath != want {
			return GateEvidence{}, errors.New("invalid deep failure log path")
		}
	}
	return gate, nil
}

func deepGateFromIntake(intake DeepIntake) GateEvidence {
	return GateEvidence{
		Target:           intake.Target,
		Level:            "deep",
		Status:           intake.Status,
		FailureLogPath:   intake.FailureLogPath,
		FirstFailingSeed: intake.FirstFailingSeed,
	}
}

func (store *fileDeepIntakeStore) Load(digest string) (DeepIntake, bool, error) {
	if !digestPattern.MatchString(digest) {
		return DeepIntake{}, false, errors.New("invalid deep intake digest")
	}
	repository, err := os.OpenRoot(store.root)
	if err != nil {
		return DeepIntake{}, false, err
	}
	defer repository.Close()
	directory, err := openStrictDir(repository, path.Join("build", "iteration", digest), false)
	if errors.Is(err, os.ErrNotExist) {
		return DeepIntake{}, false, nil
	}
	if err != nil {
		return DeepIntake{}, false, err
	}
	defer directory.Close()
	file, err := strictRegularFile(directory, "deep-intake.json")
	if errors.Is(err, os.ErrNotExist) {
		return DeepIntake{}, false, nil
	}
	if err != nil {
		return DeepIntake{}, false, err
	}
	defer file.Close()
	intake, err := decodeDeepIntake(file)
	if err != nil {
		return DeepIntake{}, false, err
	}
	if intake.DiffDigest != digest {
		return DeepIntake{}, false, errors.New("deep intake digest does not match its directory")
	}
	return intake, true, nil
}

func (store *fileDeepIntakeStore) Write(intake DeepIntake) error {
	if err := validateDeepIntake(intake); err != nil {
		return err
	}
	repository, err := os.OpenRoot(store.root)
	if err != nil {
		return err
	}
	defer repository.Close()
	directory, err := openStrictDir(
		repository,
		path.Join("build", "iteration", intake.DiffDigest),
		true,
	)
	if err != nil {
		return err
	}
	defer directory.Close()
	if info, err := directory.Lstat("deep-intake.json"); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("deep intake path is unsafe")
		}
		return errDeepIntakeExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeJSONExclusively(directory, "deep-intake.json", intake)
}

func decodeDeepIntake(reader io.Reader) (DeepIntake, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var intake DeepIntake
	if err := decoder.Decode(&intake); err != nil {
		return DeepIntake{}, fmt.Errorf("decode deep intake: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return DeepIntake{}, fmt.Errorf("decode trailing deep intake: %w", err)
		}
		return DeepIntake{}, errors.New("deep intake contains multiple JSON values")
	}
	if err := validateDeepIntake(intake); err != nil {
		return DeepIntake{}, err
	}
	return intake, nil
}

func validateDeepIntake(intake DeepIntake) error {
	if intake.SchemaVersion != 1 || !digestPattern.MatchString(intake.DiffDigest) ||
		!slices.Contains(deepTargetOrder, intake.Target) ||
		!oneOf(string(intake.Status), string(GateFail), string(GateBlocked)) ||
		!deepPlatformPattern.MatchString(intake.Platform) {
		return errors.New("invalid deep intake")
	}
	_, err := deepGateFromRunResult(intake.DiffDigest, intake.Target, RunResult{
		Status:           intake.Status,
		FailureLogPath:   intake.FailureLogPath,
		FirstFailingSeed: intake.FirstFailingSeed,
	})
	if err != nil {
		return err
	}
	return nil
}

func writeJSONExclusively(directory *os.Root, name string, value any) error {
	data, err := canonicalJSON(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := "." + name + "-" + hex.EncodeToString(nonce[:])
	file, err := directory.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Remove(temp) }()
	written, writeErr := io.Copy(file, bytes.NewReader(data))
	if writeErr == nil && written != int64(len(data)) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	if err := directory.Link(temp, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errDeepIntakeExists
		}
		return err
	}
	return nil
}

func renderDeepMarkdown(result DeepResult, writer io.Writer) error {
	var output strings.Builder
	output.WriteString("# Deep Verification\n\n")
	fmt.Fprintf(&output, "- Diff digest: `%s`\n", result.Report.DiffDigest)
	fmt.Fprintf(&output, "- Platform: `%s`\n", result.Report.Platform)
	fmt.Fprintf(&output, "- Status: `%s`\n", result.Report.Status)
	output.WriteString("\n| Target | Status | Failure log | First seed |\n")
	output.WriteString("|---|---|---|---|\n")
	if len(result.Report.Gates) == 0 {
		output.WriteString("| _None_ |  |  |  |\n")
	} else {
		for _, gate := range result.Report.Gates {
			fmt.Fprintf(
				&output,
				"| %s | %s | %s | %s |\n",
				escapeMarkdownCell(gate.Target),
				escapeMarkdownCell(string(gate.Status)),
				escapeMarkdownCell(gate.FailureLogPath),
				escapeMarkdownCell(gate.FirstFailingSeed),
			)
		}
	}
	_, err := io.WriteString(writer, output.String())
	return err
}
