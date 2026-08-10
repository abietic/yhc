// Package session — branch.go implements session branching/forking.
// Creates a new session that starts from a specific point in an existing
// session's history. Mirrors the reference --fork-session behavior from
// sessionStorage.ts and sessionRestore.ts.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/transcript"
)

// ErrMediaBranchUnsupported is retained for callers that use the lower-level
// transcript branch API. BranchSession owns prefix-exact private-media copies.
var ErrMediaBranchUnsupported = transcript.ErrMediaBranchUnsupported

// BranchOptions configures session branching behavior.
type BranchOptions struct {
	// SourceSessionID is the session to branch from.
	SourceSessionID string
	// MessageIndex is the number of messages to include from the source session
	// (1-based count: 5 means copy the first 5 messages).
	MessageIndex int
	// NewSessionID overrides the generated session ID for the branch.
	// When empty, a new UUID is generated.
	NewSessionID string
	// Dir is the session storage directory (same as used for listing/resume).
	Dir string
	// ProjectDir is the project directory for session storage path resolution.
	ProjectDir string
	// BranchName is the durable user-facing name for the child.
	BranchName string
	// OperationID makes a retried fork resolve to the same child.
	OperationID string
	// Metadata is the source execution-context snapshot to adapt for the child.
	Metadata *SessionMetadataFull
	// PlanFileIdentity is the child-owned Plan file capability. It must not
	// reuse the source session's path.
	PlanFileIdentity string
	// Clock supplies deterministic child timestamps.
	Clock func() time.Time
	// Context cancels source validation and private media copying.
	Context context.Context
}

// BranchResult contains the results of a successful branch operation.
type BranchResult struct {
	// NewSessionID is the UUID of the newly created branch session.
	NewSessionID string
	// ParentSessionID is the source session that was branched from.
	ParentSessionID string
	// MessagesCopied is how many messages were copied into the branch.
	MessagesCopied int
	// TranscriptPath is the file path of the new session's transcript.
	TranscriptPath string
	// BranchName is the durable user-facing child name.
	BranchName string
	// OperationID identifies the idempotent fork request.
	OperationID string
	// Reused reports that a prior commit for the same operation was returned.
	Reused bool
}

// BranchSession creates a new session branching from an existing session at a
// specific point in its conversation history. The new session receives a copy
// of the first `opts.MessageIndex` messages from the source session.
//
// The parent session is not modified. The new session records its parent via
// metadata (parent_session_id, branch_point).
//
// This operation is atomic: a crash during branching cannot leave a partial
// branch file on disk.
func BranchSession(opts BranchOptions) (*BranchResult, error) {
	if !isValidSessionFileID(opts.SourceSessionID) {
		return nil, errors.New("invalid source session ID")
	}
	if opts.MessageIndex <= 0 {
		return nil, errors.New("message index must be positive")
	}

	// Determine the session storage directory.
	dir := opts.Dir
	if dir == "" {
		dir = GetSessionDir(opts.ProjectDir)
	}

	// Generate new session ID if not provided.
	newID := opts.NewSessionID
	if newID == "" {
		newID = uuid.New().String()
	} else if !isValidSessionFileID(newID) {
		return nil, errors.New("invalid target session ID")
	}

	// Create the source recorder and branch using the transcript-level Branch.
	sourceRec := transcript.NewRecorder(opts.SourceSessionID, dir)
	sourcePath := sourceRec.Path()
	if sourcePath == "" {
		return nil, fmt.Errorf("cannot resolve source session path for %s", opts.SourceSessionID)
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("source session %s does not exist", opts.SourceSessionID)
		}
		return nil, fmt.Errorf("stat source session: %w", err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
		return nil, errors.New("source session transcript is not a regular non-symlink file")
	}

	loaded, err := sourceRec.LoadRefProjection()
	if err != nil {
		return nil, fmt.Errorf("load source session: %w", err)
	}
	copyCount := opts.MessageIndex
	if copyCount > len(loaded.Messages) {
		copyCount = len(loaded.Messages)
	}
	if copyCount <= 0 {
		return nil, errors.New("no messages to branch from")
	}
	now := time.Now().UTC()
	if opts.Clock != nil {
		now = opts.Clock().UTC()
	}
	childMetadata := childForkMetadata(
		opts.Metadata,
		ReadSessionMetadataFull(loaded),
		opts,
		newID,
		copyCount,
		now,
	)
	fullMetadata, err := json.Marshal(childMetadata)
	if err != nil {
		return nil, fmt.Errorf("marshal child session metadata: %w", err)
	}
	additionalMetadata := []transcript.MetadataEntry{
		{Key: "branch_name", Value: opts.BranchName, Timestamp: now},
		{Key: "branch_source_turn", Value: fmt.Sprintf("%d", copyCount), Timestamp: now},
		{Key: "forked", Value: "true", Timestamp: now},
		{Key: "fork_operation_id", Value: opts.OperationID, Timestamp: now},
		{Key: "fork_source_revision", Value: string(loaded.Revision), Timestamp: now},
		{Key: "session_metadata_full", Value: string(fullMetadata), Timestamp: now},
	}

	promptBindings := make(map[int]transcript.PromptRecordBinding)
	for _, binding := range loaded.PromptRecords {
		if binding.MessageIndex < copyCount {
			promptBindings[binding.MessageIndex] = binding
		}
	}
	targetRec := transcript.NewRecorder(newID, dir)
	if result, ok, existingErr := existingForkResult(
		targetRec,
		opts.SourceSessionID,
		opts.BranchName,
		opts.OperationID,
		loaded.Revision,
		copyCount,
		promptBindings,
	); existingErr != nil {
		return nil, existingErr
	} else if ok {
		return result, nil
	}

	targetMediaPath := targetRec.Path() + mediaSidecarSuffix
	if _, statErr := os.Lstat(targetMediaPath); statErr == nil {
		return nil, errors.New("fork target media sidecar already exists")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, errors.New("cannot inspect fork target media sidecar")
	}

	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	sourceStore := mediastore.New(sourcePath + mediaSidecarSuffix)
	uniqueRefs := make([]mediastore.Ref, 0)
	seenRefs := make(map[string]struct{})
	for _, binding := range promptBindings {
		refs, refsErr := binding.Record.MediaRefs()
		if refsErr != nil {
			return nil, fmt.Errorf("validate branch prompt: %w", refsErr)
		}
		for _, ref := range refs {
			if _, exists := seenRefs[ref.MediaID]; exists {
				continue
			}
			seenRefs[ref.MediaID] = struct{}{}
			uniqueRefs = append(uniqueRefs, ref)
		}
	}
	for _, ref := range uniqueRefs {
		data, resolveErr := sourceStore.Resolve(ctx, ref)
		if resolveErr != nil {
			return nil, fmt.Errorf("preflight branch media: %w", resolveErr)
		}
		clear(data)
	}
	if err := validateBranchSourceSnapshot(
		sourceRec,
		sourceInfo,
		loaded.Revision,
	); err != nil {
		return nil, err
	}

	replacements := make(map[string]mediastore.Ref)
	stageMediaPath := ""
	mediaInstalled := false
	if len(uniqueRefs) > 0 {
		stageMediaPath = targetMediaPath + ".stage-" + uuid.New().String()
		targetStore := mediastore.New(stageMediaPath)
		for _, ref := range uniqueRefs {
			copied, copyErr := sourceStore.CopyTo(ctx, ref, targetStore)
			if copyErr != nil {
				cleanupErr := cleanupUncommittedBranchMedia(stageMediaPath)
				if cleanupErr != nil {
					return nil, fmt.Errorf(
						"copy branch media: %w; cleanup failed: %w",
						copyErr,
						cleanupErr,
					)
				}
				return nil, fmt.Errorf("copy branch media: %w", copyErr)
			}
			replacements[ref.MediaID] = copied
		}
	}
	selected := make([]transcript.BranchMessage, 0, copyCount)
	for index, message := range loaded.Messages[:copyCount] {
		branchMessage := transcript.BranchMessage{Message: message}
		binding, rich := promptBindings[index]
		if !rich {
			selected = append(selected, branchMessage)
			continue
		}
		rewritten, rewriteErr := binding.Record.RewriteMediaRefs(replacements)
		if rewriteErr != nil {
			_ = cleanupUncommittedBranchMedia(stageMediaPath)
			return nil, fmt.Errorf("rewrite branch prompt: %w", rewriteErr)
		}
		branchMessage.PromptRecord = &rewritten
		branchMessage.RuntimeItemID = binding.RuntimeItemID
		selected = append(selected, branchMessage)
	}

	if err := validateBranchSourceSnapshot(
		sourceRec,
		sourceInfo,
		loaded.Revision,
	); err != nil {
		_ = cleanupUncommittedBranchMedia(stageMediaPath)
		return nil, err
	}
	if stageMediaPath != "" {
		if err := installStagedBranchMedia(
			stageMediaPath,
			targetMediaPath,
		); err != nil {
			stageCleanupErr := cleanupUncommittedBranchMedia(stageMediaPath)
			targetCleanupErr := cleanupUncommittedBranchMedia(targetMediaPath)
			if stageCleanupErr != nil || targetCleanupErr != nil {
				return nil, fmt.Errorf(
					"install branch media: %w; cleanup failed",
					err,
				)
			}
			return nil, fmt.Errorf("install branch media: %w", err)
		}
		mediaInstalled = true
	}

	newRec, err := sourceRec.BranchProjectionWithState(
		newID,
		selected,
		transcript.BranchState{
			Replacements:  loaded.Replacements,
			FileSnapshots: loaded.FileSnapshots,
			Metadata:      additionalMetadata,
		},
	)
	if err != nil {
		if errors.Is(err, os.ErrExist) && opts.OperationID != "" {
			if result, ok, existingErr := existingForkResult(
				targetRec,
				opts.SourceSessionID,
				opts.BranchName,
				opts.OperationID,
				loaded.Revision,
				copyCount,
				promptBindings,
			); existingErr != nil {
				return nil, existingErr
			} else if ok {
				return result, nil
			}
		}
		if mediaInstalled {
			if _, transcriptErr := os.Lstat(targetRec.Path()); errors.Is(
				transcriptErr,
				os.ErrNotExist,
			) {
				_ = cleanupUncommittedBranchMedia(targetMediaPath)
			}
		}
		return nil, fmt.Errorf("branch transcript: %w", err)
	}

	return &BranchResult{
		NewSessionID:    newID,
		ParentSessionID: opts.SourceSessionID,
		MessagesCopied:  copyCount,
		TranscriptPath:  newRec.Path(),
		BranchName:      opts.BranchName,
		OperationID:     opts.OperationID,
	}, nil
}

func validateBranchSourceSnapshot(
	recorder *transcript.Recorder,
	expectedFile os.FileInfo,
	expectedRevision transcript.TranscriptRevision,
) error {
	reloaded, err := recorder.LoadRefProjection()
	if err != nil || reloaded.Revision != expectedRevision {
		return errors.New("source session changed during branch")
	}
	current, err := os.Lstat(recorder.Path())
	if err != nil ||
		current.Mode()&os.ModeSymlink != 0 ||
		!current.Mode().IsRegular() ||
		!os.SameFile(expectedFile, current) {
		return errors.New("source session changed during branch")
	}
	return nil
}

func installStagedBranchMedia(stagePath, targetPath string) error {
	stageInfo, err := os.Lstat(stagePath)
	if err != nil || validateMediaDirectoryInfo(stageInfo) != nil {
		return errors.New("branch media staging is unavailable")
	}
	plan, err := preflightMediaDelete(stagePath)
	if err != nil || plan == nil {
		return errors.New("branch media staging is invalid")
	}
	if err := plan.revalidate(); err != nil {
		plan.close()
		return errors.New("branch media staging changed before installation")
	}
	plan.close()
	entries, err := os.ReadDir(stagePath)
	if err != nil || len(entries) != 2 {
		return errors.New("branch media staging is incomplete")
	}
	expected := map[string]bool{
		"blobs":         true,
		"manifest.json": false,
	}
	for _, entry := range entries {
		directory, ok := expected[entry.Name()]
		if !ok || entry.IsDir() != directory {
			return errors.New("branch media staging is invalid")
		}
		info, statErr := os.Lstat(filepath.Join(stagePath, entry.Name()))
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("branch media staging is invalid")
		}
		if directory {
			if validateMediaDirectoryInfo(info) != nil {
				return errors.New("branch media staging is invalid")
			}
		} else if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("branch media staging is invalid")
		}
	}
	if current, statErr := os.Lstat(stagePath); statErr != nil ||
		!os.SameFile(stageInfo, current) {
		return errors.New("branch media staging changed before installation")
	}
	if _, statErr := os.Lstat(targetPath); statErr == nil {
		return errors.New("fork target media sidecar already exists")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("cannot inspect fork target media sidecar")
	}
	if err := os.Mkdir(targetPath, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("fork target media sidecar already exists")
		}
		return errors.New("create fork target media sidecar")
	}
	if err := os.Rename(
		filepath.Join(stagePath, "blobs"),
		filepath.Join(targetPath, "blobs"),
	); err != nil {
		return fmt.Errorf("install branch media blobs: %w", err)
	}
	if err := os.Rename(
		filepath.Join(stagePath, "manifest.json"),
		filepath.Join(targetPath, "manifest.json"),
	); err != nil {
		return fmt.Errorf("install branch media manifest: %w", err)
	}
	if err := syncBranchDirectory(targetPath); err != nil {
		return fmt.Errorf("sync branch media sidecar: %w", err)
	}
	parent := filepath.Dir(targetPath)
	if err := syncBranchDirectory(parent); err != nil {
		return fmt.Errorf("sync branch media parent: %w", err)
	}
	if err := os.Remove(stagePath); err != nil {
		return fmt.Errorf("remove branch media staging: %w", err)
	}
	if err := syncBranchDirectory(parent); err != nil {
		return fmt.Errorf("sync removed branch media staging: %w", err)
	}
	return nil
}

func syncBranchDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck
	return directory.Sync()
}

func cleanupUncommittedBranchMedia(path string) error {
	if path == "" {
		return nil
	}
	plan, err := preflightMediaDelete(path)
	if err != nil || plan == nil {
		return err
	}
	defer plan.close()
	if err := plan.revalidate(); err != nil {
		return err
	}
	return plan.remove()
}

func childForkMetadata(
	explicit *SessionMetadataFull,
	persisted *SessionMetadataFull,
	opts BranchOptions,
	newID string,
	messageCount int,
	now time.Time,
) *SessionMetadataFull {
	source := explicit
	if source == nil {
		source = persisted
	}
	child := &SessionMetadataFull{}
	if source != nil {
		*child = *source
		child.AdditionalDirs = append([]string(nil), source.AdditionalDirs...)
		if source.PlanState != nil {
			planState := *source.PlanState
			child.PlanState = &planState
		}
		if source.GraphInterrupt != nil {
			graphInterrupt := *source.GraphInterrupt
			child.GraphInterrupt = &graphInterrupt
		}
		child.ModelBinding = source.ModelBinding.Clone()
	}
	sourceThreadID := child.ThreadID
	sourceAgentID := child.AgentID
	child.SessionID = newID
	child.ParentSessionID = opts.SourceSessionID
	child.ParentThreadID = firstNonEmpty(sourceThreadID, opts.SourceSessionID)
	child.ParentAgentID = sourceAgentID
	child.ParentToolUseID = ""
	child.BranchPoint = messageCount
	child.ThreadID = newID
	child.AgentID = ""
	child.AgentGeneration = 0
	child.AgentName = ""
	child.AgentRole = ""
	child.ModelRole = ""
	if child.ModelBinding != nil {
		child.ModelRole = "main"
	}
	child.Status = "idle"
	child.WorktreePath = ""
	child.WorktreeBranch = ""
	child.AgentIDs = nil
	child.PendingRequestIDs = nil
	child.GraphInterrupt = nil
	// A fork is a distinct root thread. It must not inherit the source root's
	// Goal lifecycle or accidentally activate its durable continuation state.
	child.GoalState = nil
	child.GoalBinding = nil
	child.RuntimeRevision = 0
	if child.PlanState != nil {
		child.PlanState.PlanFileIdentity = opts.PlanFileIdentity
		if child.PlanState.PlanFileIdentity == "" &&
			source != nil &&
			source.PlanState != nil &&
			source.PlanState.PlanFileIdentity != "" {
			child.PlanState.PlanFileIdentity = filepath.Join(
				filepath.Dir(source.PlanState.PlanFileIdentity),
				newID+".md",
			)
		}
		if child.PlanState.Phase == "awaiting_approval" {
			child.PlanState.Phase = "active"
			child.PlanState.ApprovalRequestID = ""
			if child.PlanState.Revision < ^uint64(0) {
				child.PlanState.Revision++
			}
		}
	}
	child.CreatedAt = now
	child.UpdatedAt = now
	child.MessageCount = messageCount
	if source != nil &&
		(source.MessageCount == 0 || messageCount >= source.MessageCount) {
		child.TokenUsage = source.TokenUsage
	} else {
		child.TokenUsage = 0
	}
	child.IsLeaf = true
	return child
}

func existingForkResult(
	recorder *transcript.Recorder,
	parentSessionID string,
	branchName string,
	operationID string,
	sourceRevision transcript.TranscriptRevision,
	messageCount int,
	sourcePrompts map[int]transcript.PromptRecordBinding,
) (*BranchResult, bool, error) {
	info, err := os.Lstat(recorder.Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("inspect fork target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf(
			"fork target is not a regular file: %s",
			recorder.Path(),
		)
	}
	if operationID == "" {
		return nil, false, fmt.Errorf("fork target already exists: %s", recorder.Path())
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		return nil, false, fmt.Errorf("load existing fork target: %w", err)
	}
	metadata := make(map[string]string)
	for _, entry := range loaded.Metadata {
		metadata[entry.Key] = entry.Value
	}
	if metadata["fork_operation_id"] != operationID ||
		metadata["parent_session_id"] != parentSessionID ||
		metadata["branch_source_turn"] != strconv.Itoa(messageCount) ||
		len(loaded.Messages) != messageCount {
		return nil, false, fmt.Errorf(
			"fork target %s belongs to another operation",
			recorder.SessionID,
		)
	}
	storedRevision := metadata["fork_source_revision"]
	if storedRevision != "" && storedRevision != string(sourceRevision) {
		return nil, false, errors.New(
			"fork target source revision does not match",
		)
	}
	if loaded.HasMediaRefs && storedRevision == "" {
		return nil, false, errors.New(
			"fork target has no source revision binding",
		)
	}
	if err := validateForkPromptMapping(sourcePrompts, loaded.PromptRecords); err != nil {
		return nil, false, err
	}
	return &BranchResult{
		NewSessionID:    recorder.SessionID,
		ParentSessionID: parentSessionID,
		MessagesCopied:  len(loaded.Messages),
		TranscriptPath:  recorder.Path(),
		BranchName:      firstNonEmpty(metadata["branch_name"], branchName),
		OperationID:     operationID,
		Reused:          true,
	}, true, nil
}

func validateForkPromptMapping(
	source map[int]transcript.PromptRecordBinding,
	child []transcript.PromptRecordBinding,
) error {
	childByIndex := make(map[int]transcript.PromptRecordBinding, len(child))
	for _, binding := range child {
		if _, exists := childByIndex[binding.MessageIndex]; exists {
			return errors.New("fork target prompt mapping is ambiguous")
		}
		childByIndex[binding.MessageIndex] = binding
	}
	if len(childByIndex) != len(source) {
		return errors.New("fork target prompt mapping does not match")
	}
	sourceToChild := make(map[string]string)
	childToSource := make(map[string]string)
	for messageIndex, expected := range source {
		actual, ok := childByIndex[messageIndex]
		if !ok ||
			actual.RuntimeItemID != expected.RuntimeItemID ||
			actual.Record.Version != expected.Record.Version ||
			actual.Record.TurnID != expected.Record.TurnID ||
			len(actual.Record.Parts) != len(expected.Record.Parts) {
			return errors.New("fork target prompt mapping does not match")
		}
		for partIndex, expectedPart := range expected.Record.Parts {
			actualPart := actual.Record.Parts[partIndex]
			if actualPart.Kind != expectedPart.Kind {
				return errors.New("fork target prompt mapping does not match")
			}
			switch expectedPart.Kind {
			case promptrecord.PartText:
				if expectedPart.Text == nil ||
					actualPart.Text == nil ||
					expectedPart.Text.Text != actualPart.Text.Text {
					return errors.New("fork target prompt mapping does not match")
				}
			case promptrecord.PartImage:
				if expectedPart.Image == nil ||
					actualPart.Image == nil ||
					expectedPart.Image.Detail != actualPart.Image.Detail ||
					!reflect.DeepEqual(
						expectedPart.Image.Annotations,
						actualPart.Image.Annotations,
					) ||
					!forkRefMetadataEqual(
						expectedPart.Image.Ref,
						actualPart.Image.Ref,
					) ||
					expectedPart.Image.Ref.MediaID ==
						actualPart.Image.Ref.MediaID {
					return errors.New("fork target prompt mapping does not match")
				}
				if err := rememberForkRef(
					expectedPart.Image.Ref,
					actualPart.Image.Ref,
					sourceToChild,
					childToSource,
				); err != nil {
					return err
				}
			case promptrecord.PartResourceLink:
				if !reflect.DeepEqual(
					expectedPart.ResourceLink,
					actualPart.ResourceLink,
				) {
					return errors.New("fork target prompt mapping does not match")
				}
			case promptrecord.PartEmbeddedText:
				if !reflect.DeepEqual(
					expectedPart.EmbeddedText,
					actualPart.EmbeddedText,
				) {
					return errors.New("fork target prompt mapping does not match")
				}
			case promptrecord.PartEmbeddedBlob:
				if expectedPart.EmbeddedBlob == nil ||
					actualPart.EmbeddedBlob == nil {
					return errors.New("fork target prompt mapping does not match")
				}
				expectedBlob := *expectedPart.EmbeddedBlob
				actualBlob := *actualPart.EmbeddedBlob
				expectedRef := expectedBlob.Ref
				actualRef := actualBlob.Ref
				expectedBlob.Ref = mediastore.Ref{}
				actualBlob.Ref = mediastore.Ref{}
				if !reflect.DeepEqual(expectedBlob, actualBlob) ||
					!forkRefMetadataEqual(expectedRef, actualRef) ||
					expectedRef.MediaID == actualRef.MediaID {
					return errors.New("fork target prompt mapping does not match")
				}
				if err := rememberForkRef(
					expectedRef,
					actualRef,
					sourceToChild,
					childToSource,
				); err != nil {
					return err
				}
			default:
				return errors.New("fork target prompt mapping is unsupported")
			}
		}
	}
	return nil
}

func rememberForkRef(
	source mediastore.Ref,
	child mediastore.Ref,
	sourceToChild map[string]string,
	childToSource map[string]string,
) error {
	if mapped, exists := sourceToChild[source.MediaID]; exists &&
		mapped != child.MediaID {
		return errors.New("fork target prompt mapping is inconsistent")
	}
	if mapped, exists := childToSource[child.MediaID]; exists &&
		mapped != source.MediaID {
		return errors.New("fork target prompt mapping is inconsistent")
	}
	sourceToChild[source.MediaID] = child.MediaID
	childToSource[child.MediaID] = source.MediaID
	return nil
}

func forkRefMetadataEqual(left, right mediastore.Ref) bool {
	return left.Version == right.Version &&
		left.MIMEType == right.MIMEType &&
		left.SizeBytes == right.SizeBytes &&
		left.Width == right.Width &&
		left.Height == right.Height
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// ListBranches finds all sessions that were branched from the given parent
// session ID by scanning session files for parent_session_id metadata.
// This is a lightweight scan that reads only the first few KB of each file.
func ListBranches(parentSessionID, dir string) ([]string, error) {
	if parentSessionID == "" {
		return nil, errors.New("parent session ID must not be empty")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read session dir: %w", err)
	}

	// The parent metadata pattern we're searching for.
	parentPattern := fmt.Sprintf(`"meta_key":"parent_session_id","meta_value":"%s"`, parentSessionID)
	parentPatternSpaced := fmt.Sprintf(`"meta_key": "parent_session_id", "meta_value": "%s"`, parentSessionID)

	var branches []string
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".jsonl" {
			continue
		}
		sessionID := name[:len(name)-len(".jsonl")]
		if sessionID == parentSessionID {
			continue // Don't list the parent itself.
		}

		// Read a small portion of the file to check for parent metadata.
		path := filepath.Join(dir, name)
		data, err := readFileHead(path, 32*1024) // 32KB should cover metadata entries
		if err != nil {
			continue
		}
		content := string(data)
		if contains(content, parentPattern) || contains(content, parentPatternSpaced) {
			branches = append(branches, sessionID)
		}
	}

	return branches, nil
}

// SessionLineage holds the parent/child relationship information for a session.
type SessionLineage struct {
	// ParentSessionID is the session this was branched from (empty if root).
	ParentSessionID string
	// BranchPoint is the message index where the branch occurred.
	BranchPoint int
	// Children are session IDs that branched from this session.
	Children []string
	// IsLeaf is true if no other sessions have branched from this one.
	IsLeaf bool
	// CreatedAt is when this session was created.
	CreatedAt time.Time
}

// GetSessionLineage retrieves the lineage information for a session.
func GetSessionLineage(sessionID, dir string) (*SessionLineage, error) {
	if sessionID == "" {
		return nil, errors.New("session ID must not be empty")
	}

	rec := transcript.NewRecorder(sessionID, dir)
	result, err := rec.LoadFull()
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}

	lineage := &SessionLineage{}

	// Extract parent info from metadata.
	for _, meta := range result.Metadata {
		switch meta.Key {
		case "parent_session_id":
			lineage.ParentSessionID = meta.Value
		case "branch_point":
			_, _ = fmt.Sscanf(meta.Value, "%d", &lineage.BranchPoint)
		}
		if lineage.CreatedAt.IsZero() {
			lineage.CreatedAt = meta.Timestamp
		}
	}

	// Find children (sessions that branched from this one).
	children, err := ListBranches(sessionID, dir)
	if err != nil {
		// Non-fatal: we can still return what we have.
		children = nil
	}
	lineage.Children = children
	lineage.IsLeaf = len(children) == 0

	return lineage, nil
}

// --- helpers ---

// readFileHead reads up to maxBytes from the beginning of a file.
func readFileHead(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	buf := make([]byte, maxBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// contains is a simple string-in-string check (avoids importing strings for one use).
func contains(s, substr string) bool {
	return len(substr) <= len(s) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// PersistedPlanStateVersion is the latest additive Plan checkpoint schema.
// Older readers ignore the nested record while newer readers can normalize
// unsupported versions without treating an approval reference as authority.
const PersistedPlanStateVersion = 1

// PersistedPlanState is the durable, presentation-free Plan snapshot. It never
// contains callback handles, terminal approval decisions, or permission grants.
type PersistedPlanState struct {
	Version               int    `json:"version"`
	Phase                 string `json:"phase"`
	PlanFileIdentity      string `json:"plan_file_identity"`
	ReturnMode            string `json:"return_mode"`
	ApprovalRequestID     string `json:"approval_request_id,omitempty"`
	ApprovalInitialDigest string `json:"approval_initial_digest,omitempty"`
	Revision              uint64 `json:"revision"`
}

const (
	PersistedGoalStateLegacyVersion          uint16 = 1
	PersistedGoalStateAccountingVersion      uint16 = 2
	PersistedGoalStateContinuationVersion    uint16 = 3
	PersistedGoalStateVersion                uint16 = 4
	PersistedGoalBindingVersion              uint16 = 1
	PersistedGoalUsageAdmissionLegacyVersion uint16 = 1
	PersistedGoalUsageAdmissionVersion       uint16 = 2
	PersistedGoalContinuationLegacyVersion   uint16 = 1
	PersistedGoalContinuationVersion         uint16 = 2
)

// PersistedGoalUsageAdmission is the positive-version durable gate written
// before one Goal-bound provider entry. It contains identity only; final usage
// is committed separately in the root transcript ledger.
type PersistedGoalUsageAdmission struct {
	Version                  uint16    `json:"version"`
	LedgerRevision           uint64    `json:"ledger_revision"`
	GoalID                   string    `json:"goal_id"`
	ObjectiveRevision        uint64    `json:"objective_revision"`
	RootSessionID            string    `json:"root_session_id"`
	RootThreadID             string    `json:"root_thread_id"`
	RootAgentID              string    `json:"root_agent_id,omitempty"`
	ExecutingSessionID       string    `json:"executing_session_id"`
	ExecutingThreadID        string    `json:"executing_thread_id"`
	ExecutingAgentID         string    `json:"executing_agent_id,omitempty"`
	ExecutingAgentGeneration int64     `json:"executing_agent_generation,omitempty"`
	GoalTurnID               string    `json:"goal_turn_id"`
	LogicalRoundID           string    `json:"logical_round_id"`
	LogicalRequestID         string    `json:"logical_request_id"`
	ModelAttemptID           string    `json:"model_attempt_id"`
	ModelAttemptIndex        int       `json:"model_attempt_index"`
	ModelProfile             string    `json:"model_profile"`
	ModelRetryIndex          int       `json:"model_retry_index"`
	ProviderCallID           string    `json:"provider_call_id"`
	AdmittedAt               time.Time `json:"admitted_at"`
	rawEncoding              json.RawMessage
}

// UnmarshalJSON retains the exact legacy admission object so fail-closed
// recovery cannot rewrite unresolved in-flight evidence during a later Session
// checkpoint.
func (a *PersistedGoalUsageAdmission) UnmarshalJSON(data []byte) error {
	type persistedGoalUsageAdmissionAlias PersistedGoalUsageAdmission
	var decoded persistedGoalUsageAdmissionAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*a = PersistedGoalUsageAdmission(decoded)
	if a.Version == PersistedGoalUsageAdmissionLegacyVersion {
		a.rawEncoding = append(json.RawMessage(nil), data...)
	}
	return nil
}

// MarshalJSON preserves an unsupported legacy in-flight admission byte for
// byte while newer admissions use the canonical current schema.
func (a PersistedGoalUsageAdmission) MarshalJSON() ([]byte, error) {
	if a.Version == PersistedGoalUsageAdmissionLegacyVersion &&
		json.Valid(a.rawEncoding) {
		return append([]byte(nil), a.rawEncoding...), nil
	}
	type persistedGoalUsageAdmissionAlias PersistedGoalUsageAdmission
	return json.Marshal(persistedGoalUsageAdmissionAlias(a))
}

// PersistedGoalContinuation is the recoverable immutable cursor for one
// terminal-derived Goal continuation. It contains identity and disposition
// only; the RuntimeInputCoordinator separately owns delivery state.
type PersistedGoalContinuation struct {
	Version                     uint16    `json:"version"`
	ItemID                      string    `json:"item_id"`
	CheckpointID                string    `json:"checkpoint_id"`
	ContinuationTurnID          string    `json:"continuation_turn_id"`
	GoalID                      string    `json:"goal_id"`
	GoalSchemaVersion           uint16    `json:"goal_schema_version"`
	ObjectiveRevision           uint64    `json:"objective_revision"`
	GoalRevision                uint64    `json:"goal_revision"`
	GoalStatus                  string    `json:"goal_status"`
	RootSessionID               string    `json:"root_session_id"`
	RootThreadID                string    `json:"root_thread_id"`
	RootAgentID                 string    `json:"root_agent_id,omitempty"`
	PredecessorGoalTurnID       string    `json:"predecessor_goal_turn_id"`
	PredecessorTerminalSequence uint64    `json:"predecessor_terminal_sequence"`
	PredecessorTerminalReason   string    `json:"predecessor_terminal_reason"`
	PredecessorTerminalAt       time.Time `json:"predecessor_terminal_at"`
	TokenBudget                 *uint64   `json:"token_budget,omitempty"`
	TokensUsed                  uint64    `json:"tokens_used"`
	UsageLedgerRevision         uint64    `json:"usage_ledger_revision"`
	ContinuationOrdinal         uint64    `json:"continuation_ordinal"`
	RuntimeRevision             uint64    `json:"runtime_revision"`
	Disposition                 string    `json:"disposition"`
	DispositionReason           string    `json:"disposition_reason,omitempty"`
	DispositionAt               time.Time `json:"disposition_at,omitempty"`
}

// PersistedGoalBinding attributes one child Session generation to the exact
// root Goal turn that created it. It is inert identity only and carries no
// mutation, continuation, provider, or permission authority.
type PersistedGoalBinding struct {
	Version           uint16 `json:"version"`
	GoalID            string `json:"goal_id"`
	ObjectiveRevision uint64 `json:"objective_revision"`
	RootSessionID     string `json:"root_session_id"`
	RootThreadID      string `json:"root_thread_id"`
	RootAgentID       string `json:"root_agent_id,omitempty"`
	GoalTurnID        string `json:"goal_turn_id"`
}

// PersistedGoalState is the durable, presentation-free snapshot for one root
// Session Goal. It contains stable data only; callbacks, model/provider
// handles, tool registries, permission authority, and execution grants are
// never persisted here.
type PersistedGoalState struct {
	Version                          uint16                       `json:"version"`
	GoalID                           string                       `json:"goal_id"`
	Objective                        string                       `json:"objective"`
	ObjectiveRevision                uint64                       `json:"objective_revision"`
	Status                           string                       `json:"status"`
	StatusReasonCode                 string                       `json:"status_reason_code,omitempty"`
	StatusReason                     string                       `json:"status_reason,omitempty"`
	Revision                         uint64                       `json:"revision"`
	TokenBudget                      *uint64                      `json:"token_budget,omitempty"`
	TokensUsed                       uint64                       `json:"tokens_used,omitempty"`
	UsageLedgerRevision              uint64                       `json:"usage_ledger_revision,omitempty"`
	RootActiveTimeMillis             int64                        `json:"root_active_time_millis,omitempty"`
	ContinuationOrdinal              uint64                       `json:"continuation_ordinal,omitempty"`
	Continuation                     *PersistedGoalContinuation   `json:"continuation,omitempty"`
	LastGoalTurnID                   string                       `json:"last_goal_turn_id,omitempty"`
	LastTerminalSequence             uint64                       `json:"last_terminal_sequence,omitempty"`
	PendingCompleteTurnID            string                       `json:"pending_complete_turn_id,omitempty"`
	PendingCompleteObjectiveRevision uint64                       `json:"pending_complete_objective_revision,omitempty"`
	PendingUsageAdmission            *PersistedGoalUsageAdmission `json:"pending_usage_admission,omitempty"`
	BlockerKey                       string                       `json:"blocker_key,omitempty"`
	BlockerTurnIDs                   []string                     `json:"blocker_turn_ids,omitempty"`
	CreatedAt                        time.Time                    `json:"created_at"`
	UpdatedAt                        time.Time                    `json:"updated_at"`
	invalidEncoding                  bool
	rawEncoding                      json.RawMessage
}

// UnmarshalJSON contains malformed-but-valid nested Goal JSON so one damaged
// optional record cannot make the enclosing Session metadata unreadable.
// Semantic validation and fail-closed recovery remain the engine's job.
func (s *PersistedGoalState) UnmarshalJSON(data []byte) error {
	type persistedGoalStateAlias PersistedGoalState
	var decoded persistedGoalStateAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		*s = PersistedGoalState{
			invalidEncoding: true,
			rawEncoding:     append(json.RawMessage(nil), data...),
		}
		return nil
	}
	*s = PersistedGoalState(decoded)
	return nil
}

// MarshalJSON preserves an unavailable malformed record as its original JSON
// value when a later Session checkpoint rewrites otherwise healthy metadata.
func (s PersistedGoalState) MarshalJSON() ([]byte, error) {
	if s.invalidEncoding && json.Valid(s.rawEncoding) {
		return append([]byte(nil), s.rawEncoding...), nil
	}
	type persistedGoalStateAlias PersistedGoalState
	return json.Marshal(persistedGoalStateAlias(s))
}

// HasInvalidEncoding reports whether nested Goal JSON could not be decoded.
// Callers must keep such state inert and may only preserve or explicitly clear
// it.
func (s *PersistedGoalState) HasInvalidEncoding() bool {
	return s != nil && s.invalidEncoding
}

// PersistedGraphInterrupt mirrors only the stable identity of one project
// Graph interrupt. Exact invocation data remains in the protected Eino
// checkpoint sidecar and this record never grants permission.
type PersistedGraphInterrupt struct {
	Version          int    `json:"version"`
	RequestID        string `json:"request_id"`
	InterruptID      string `json:"interrupt_id"`
	InvocationDigest string `json:"invocation_digest"`
	PolicyRevision   string `json:"policy_revision"`
	Kind             string `json:"kind"`
}

// SessionMetadataFull holds extended metadata for a session including lineage info.
// This is persisted as metadata entries in the transcript JSONL.
type SessionMetadataFull struct {
	// SessionID is the unique identifier.
	SessionID string `json:"session_id"`
	// ParentSessionID is set when this session was branched from another.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	ParentThreadID  string `json:"parent_thread_id,omitempty"`
	ParentAgentID   string `json:"parent_agent_id,omitempty"`
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
	// BranchPoint is the message index where the branch occurred.
	BranchPoint int `json:"branch_point,omitempty"`
	// Model is the model identifier used in this session.
	Model string `json:"model,omitempty"`
	// Provider is the model provider (e.g., "openai", "claude", "ark").
	Provider string `json:"provider,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	// AgentGeneration identifies the current durable execution generation for
	// this child Session. Agent metadata and GoalBinding admission must name
	// the same generation before restore projects descendant attribution.
	AgentGeneration int64  `json:"agent_generation,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	AgentRole       string `json:"agent_role,omitempty"`
	// ModelRole is the fixed provider-routing role admitted for this Session.
	// AgentRole remains the original Agent type that owns prompt/tool policy.
	ModelRole      string `json:"model_role,omitempty"`
	Status         string `json:"status,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	// QueryKernelVersion pins one query-loop owner for the lifetime of this
	// session. Unknown versions must fail closed rather than reinterpret the
	// transcript through another kernel.
	QueryKernelVersion string `json:"query_kernel_version,omitempty"`
	// QueryKernelStage keeps the historical JSON key so existing transcripts
	// remain byte/schema compatible after active rollout vocabulary is retired.
	QueryKernelStage           string `json:"query_kernel_canary_stage,omitempty"`
	QueryKernelIncompatibility string `json:"query_kernel_incompatibility,omitempty"`
	// PlanState is additive and versioned independently from the outer session
	// metadata so older binaries continue to read the existing fields.
	PlanState *PersistedPlanState `json:"plan_state,omitempty"`
	// GoalState is additive and versioned independently from outer Session
	// metadata. Older readers ignore it and retain ordinary Session behavior.
	GoalState *PersistedGoalState `json:"goal_state,omitempty"`
	// GoalBinding is present only on Goal-bound descendant Sessions. The root
	// Goal remains authoritative and child readers cannot turn this identity
	// into mutation authority.
	GoalBinding    *PersistedGoalBinding    `json:"goal_binding,omitempty"`
	GraphInterrupt *PersistedGraphInterrupt `json:"graph_interrupt,omitempty"`
	// ModelBinding is the additive logical route identity. Unsupported nested
	// versions remain opaque and inert until an explicit successful rebind.
	ModelBinding   *PersistedModelBinding `json:"model_binding,omitempty"`
	WorktreePath   string                 `json:"worktree_path,omitempty"`
	WorktreeBranch string                 `json:"worktree_branch,omitempty"`
	// AdditionalDirs restores the explicitly expanded working scope.
	AdditionalDirs []string `json:"additional_dirs,omitempty"`
	// AgentIDs identifies child Agent records associated with this session.
	AgentIDs []string `json:"agent_ids,omitempty"`
	// PendingRequestIDs are diagnostic references only. Resume must intersect
	// them with live runtime state and must never recreate requests from disk.
	PendingRequestIDs []string `json:"pending_request_ids,omitempty"`
	// RuntimeRevision identifies the runtime snapshot used by this checkpoint.
	RuntimeRevision uint64 `json:"runtime_revision,omitempty"`
	// CreatedAt is when the session was first created.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the last modification time.
	UpdatedAt time.Time `json:"updated_at"`
	// MessageCount is the total number of messages in the session.
	MessageCount int `json:"message_count"`
	// TokenUsage is the approximate total token count.
	TokenUsage int `json:"token_usage,omitempty"`
	// IsLeaf indicates whether this session has any branches.
	IsLeaf bool `json:"is_leaf"`
	// GitBranch is the git branch at session creation time.
	GitBranch string `json:"git_branch,omitempty"`
	// CWD is the working directory at session creation time.
	CWD string `json:"cwd,omitempty"`
}

// WriteSessionMetadata persists full session metadata as a single JSON metadata
// entry in the transcript. This is called periodically (e.g., after each turn)
// to keep the metadata up to date.
func WriteSessionMetadata(rec *transcript.Recorder, meta *SessionMetadataFull) error {
	if rec == nil || meta == nil {
		return nil
	}

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	return rec.RecordMetadata("session_metadata_full", string(data))
}

// ReadSessionMetadataFull extracts the latest full metadata entry from a loaded transcript.
// Returns nil if no full metadata entry exists.
func ReadSessionMetadataFull(result *transcript.LoadResult) *SessionMetadataFull {
	if result == nil {
		return nil
	}

	// Find the last session_metadata_full entry (last-wins semantics).
	var lastValue string
	for _, meta := range result.Metadata {
		if meta.Key == "session_metadata_full" {
			lastValue = meta.Value
		}
	}
	if lastValue == "" {
		return nil
	}

	var full SessionMetadataFull
	if err := json.Unmarshal([]byte(lastValue), &full); err != nil {
		return nil
	}
	return &full
}
