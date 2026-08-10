package transcript

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/internal/providerorigin"
	"github.com/cloudwego/eino/schema"
)

// Recorder persists messages to session storage for --resume support.
type Recorder struct {
	SessionID string
	Dir       string

	mu                       sync.Mutex
	file                     *os.File
	parentDirSyncPending     bool
	partialLineRepairPending bool
	messageIdentities        map[*schema.Message][]trackedMessageIdentity
	promptRecords            map[*schema.Message]promptrecord.Record
	assistantOrigins         map[*schema.Message]providerorigin.BindingResolution
	pendingAssistantOrigins  map[*schema.Message]providerorigin.Origin
	syncFile                 func(*os.File) error
	syncDir                  func(string) error
	beforeEncode             func(string) error
}

// DurabilityUncertainError means a write reached the operating system, but its
// durable outcome could not be established. Callers must not describe this as
// a clean pre-commit failure: the process should fail closed and let replay
// determine the visible state after storage recovers.
type DurabilityUncertainError struct {
	Operation string
	Err       error
}

// ErrMediaBranchUnsupported is the fail-closed guard for the low-level
// transcript-only branch API. The Session lifecycle layer owns media copying.
var ErrMediaBranchUnsupported = errors.New("media_branch_unsupported")

var durablePromptKindPattern = regexp.MustCompile(
	`"kind"[ \t\r\n]*:[ \t\r\n]*"user-prompt"`,
)

func (e *DurabilityUncertainError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s durability is uncertain: %v", e.Operation, e.Err)
}

func (e *DurabilityUncertainError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsDurabilityUncertain reports whether a recorder error occurred after bytes
// may already have reached the transcript.
func IsDurabilityUncertain(err error) bool {
	var uncertain *DurabilityUncertainError
	return errors.As(err, &uncertain)
}

type recordEntry struct {
	Timestamp      time.Time             `json:"timestamp"`
	EntryID        *EntryIdentity        `json:"entry_id,omitempty"`
	Kind           string                `json:"kind,omitempty"`
	Message        *schema.Message       `json:"message,omitempty"`
	Messages       []*schema.Message     `json:"messages,omitempty"`
	UserPrompt     *promptrecord.Record  `json:"user_prompt,omitempty"`
	RuntimeItemID  string                `json:"runtime_item_id,omitempty"`
	PromptMessages []promptMessageRecord `json:"prompt_messages,omitempty"`
	Replacements   []Replacement         `json:"replacements,omitempty"`
	// Metadata fields (used when Kind == "metadata")
	MetaKey   string `json:"meta_key,omitempty"`
	MetaValue string `json:"meta_value,omitempty"`
	// File history snapshot fields (used when Kind == "file-history-snapshot")
	FileStates map[string]FileState `json:"file_states,omitempty"`
	// Usage is a cumulative provider-reported snapshot written with lifecycle
	// boundaries. It closes checkpoint/compaction repair gaps without treating
	// repeated boundary messages as new model calls.
	Usage     *UsageSummary    `json:"usage,omitempty"`
	GoalUsage *GoalUsageRecord `json:"goal_usage,omitempty"`
	// AssistantOrigins is private transcript metadata. Public projections use
	// durableEntryFromRecord and intentionally never receive this field.
	AssistantOrigins       *assistantOriginEnvelope `json:"assistant_origins,omitempty"`
	assistantOriginSource  *schema.Message
	assistantOriginSources []*schema.Message
}

type promptMessageRecord struct {
	Index         int                 `json:"index"`
	Prompt        promptrecord.Record `json:"prompt"`
	RuntimeItemID string              `json:"runtime_item_id,omitempty"`
}

var runtimeItemDeliveryIDPattern = regexp.MustCompile(
	`^[A-Za-z0-9._:-]{1,128}$`,
)

func validRuntimeItemDeliveryID(id string) bool {
	return runtimeItemDeliveryIDPattern.MatchString(id)
}

func runtimeItemDeliveryID(message *schema.Message) string {
	if message == nil || message.Extra == nil {
		return ""
	}
	for _, key := range []string{"runtime_item_id", "command_uuid"} {
		id, _ := message.Extra[key].(string)
		id = strings.TrimSpace(id)
		if validRuntimeItemDeliveryID(id) {
			return id
		}
	}
	return ""
}

func attachRuntimeItemDeliveryIdentity(
	message *schema.Message,
	runtimeItemID string,
) {
	if message == nil || runtimeItemID == "" {
		return
	}
	if message.Extra == nil {
		message.Extra = make(map[string]any)
	}
	message.Extra["runtime_item_id"] = runtimeItemID
	message.Extra["command_uuid"] = runtimeItemID
}

func (r *promptMessageRecord) UnmarshalJSON(data []byte) error {
	type wirePromptMessageRecord promptMessageRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded wirePromptMessageRecord
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("durable prompt snapshot has trailing data")
	}
	if decoded.RuntimeItemID != "" &&
		!validRuntimeItemDeliveryID(decoded.RuntimeItemID) {
		return errors.New("durable prompt snapshot has invalid runtime item identity")
	}
	*r = promptMessageRecord(decoded)
	return nil
}

// LifecycleBoundaryKind identifies one durable active-context transition.
// Boundary records are additive: older readers ignore them, while newer
// readers replay the latest snapshot without deleting prior transcript data.
type LifecycleBoundaryKind string

const (
	LifecycleSessionStart LifecycleBoundaryKind = "session-start"
	LifecycleReset        LifecycleBoundaryKind = "reset-boundary"
	LifecycleCompact      LifecycleBoundaryKind = "compact-boundary"
	LifecycleCheckpoint   LifecycleBoundaryKind = "state-checkpoint"
)

// LifecycleBoundary is one replayable active-context snapshot retained in the
// append-only transcript audit.
type LifecycleBoundary struct {
	Timestamp    time.Time
	Kind         LifecycleBoundaryKind
	Messages     []*schema.Message
	Replacements []Replacement
	FileStates   map[string]FileState
	Usage        *UsageSummary
}

// Replacement records a single tool result replacement decision.
// Mirrors ContentReplacementRecord from the reference (toolResultStorage.ts:475-479).
type Replacement struct {
	ToolUseID   string `json:"tool_use_id"`
	Replacement string `json:"replacement"`
}

// FileState records the state of a file at a point in time (for resume reconstruction).
type FileState struct {
	Path     string `json:"path"`
	WasRead  bool   `json:"was_read,omitempty"`
	WasEdit  bool   `json:"was_edit,omitempty"`
	WasWrite bool   `json:"was_write,omitempty"`
}

// MetadataEntry holds a key-value metadata pair recorded in the transcript.
type MetadataEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

// CorruptionInfo describes a single corruption event found during transcript loading.
type CorruptionInfo struct {
	// Line is the 1-based line number where corruption was found.
	Line int
	// Err is the error that occurred while parsing the line.
	Err error
	// RawLine is the (possibly truncated) content of the corrupt line.
	RawLine string
}

// LoadResult holds all data loaded from a transcript file.
type LoadResult struct {
	// Revision fingerprints the exact bytes of the opened transcript. Entries
	// contains every valid physical JSONL record in source order.
	Revision TranscriptRevision
	Entries  []DurableEntry
	// Messages, Replacements and FileSnapshots describe the active context
	// after replaying all append-only lifecycle boundaries. Earlier snapshots
	// remain available in LifecycleBoundaries and in the underlying JSONL audit.
	Messages                []*schema.Message
	Replacements            []Replacement
	Metadata                []MetadataEntry
	FileSnapshots           []map[string]FileState
	LifecycleBoundaries     []LifecycleBoundary
	AgentCompletionReceipts []AgentCompletionReceipt
	// GoalUsageRecords is the append-only provider-attribution ledger. It is
	// separate from the cumulative Session Usage diagnostic above.
	GoalUsageRecords []GoalUsageRecord
	// Usage aggregates provider metadata from append-only message entries and
	// the latest cumulative lifecycle snapshot. It contains no estimates.
	Usage UsageSummary
	// Corruptions lists any corrupt/malformed entries skipped during loading.
	// When non-empty, the load succeeded partially — valid entries were recovered.
	Corruptions []CorruptionInfo
	// GoalUsageCorruptions identifies malformed records that may have been
	// intended for the Goal ledger. Goal recovery treats any such coverage as
	// fail-closed instead of borrowing Session totals.
	GoalUsageCorruptions []CorruptionInfo
	// HasMediaRefs reports that at least one valid physical record references
	// the Session-private media sidecar. Lifecycle operations use this as a
	// fail-closed compatibility gate until P30.2c owns ref copying and paging.
	HasMediaRefs bool
	// MediaMessageIndexes identifies ref-backed prompts in the final active
	// message projection. Indexes use the same zero-based order as Messages.
	MediaMessageIndexes []int
	// PromptRecords binds ref-backed prompts to the final active message
	// projection. The records retain private refs for trusted lifecycle
	// consumers; presentation callers must project sanitized descriptors.
	PromptRecords []PromptRecordBinding
	// AllPromptRecords retains every valid physical transcript ref, including
	// superseded lifecycle snapshots. Reachability GC must preserve them while
	// the append-only audit file still contains them.
	AllPromptRecords []PromptRecordBinding
}

// PromptRecordBinding identifies one ref-backed prompt in the final active
// message projection without resolving its private media bytes.
type PromptRecordBinding struct {
	MessageIndex  int
	Record        promptrecord.Record
	RuntimeItemID string
}

// PromptRecordBindings returns recorder-owned prompt records for the exact
// message objects in one active projection. It never resolves or copies media
// bytes and never infers identity from message content or position.
func (r *Recorder) PromptRecordBindings(
	messages []*schema.Message,
) []PromptRecordBinding {
	if r == nil || len(messages) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	bindings := make([]PromptRecordBinding, 0)
	for index, message := range messages {
		record, ok := r.promptRecords[message]
		if !ok {
			continue
		}
		bindings = append(bindings, PromptRecordBinding{
			MessageIndex:  index,
			Record:        record.Clone(),
			RuntimeItemID: runtimeItemDeliveryID(message),
		})
	}
	return bindings
}

// NewRecorder creates a new transcript recorder.
func NewRecorder(sessionID, dir string) *Recorder {
	return &Recorder{SessionID: sessionID, Dir: dir}
}

// Path returns the on-disk transcript path for the current session.
func (r *Recorder) Path() string {
	if r == nil || r.SessionID == "" || r.Dir == "" {
		return ""
	}
	return filepath.Join(r.Dir, r.SessionID+".jsonl")
}

// Load reads the full stored transcript for the session.
func (r *Recorder) Load() ([]*schema.Message, error) {
	result, err := r.LoadFull()
	if err != nil {
		return nil, err
	}
	return result.Messages, nil
}

// LoadFull reads the transcript and returns messages plus metadata entries.
// It is corruption-tolerant: malformed/truncated JSONL lines are skipped and
// recorded in LoadResult.Corruptions rather than causing a hard failure.
// This mirrors the reference's graceful handling of partial writes from crashes.
func (r *Recorder) LoadFull() (*LoadResult, error) {
	return r.LoadFullContext(context.Background())
}

// LoadFullContext is LoadFull with cancellation propagated through private
// media resolution.
func (r *Recorder) LoadFullContext(ctx context.Context) (*LoadResult, error) {
	if r == nil {
		return &LoadResult{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result, entries, err := loadTranscriptFileContext(ctx, r.Path())
	if err != nil {
		return nil, err
	}
	r.trackLoadedMessageIdentitiesLocked(entries)
	if err := r.rebindAssistantOriginsLocked(
		activeTranscriptMessageSources(entries),
		result.Messages,
	); err != nil {
		return nil, err
	}
	return result, nil
}

func activeTranscriptMessageSources(entries []recordEntry) []*schema.Message {
	messages := make([]*schema.Message, 0)
	for _, entry := range entries {
		if entry.Message != nil {
			messages = append(messages, entry.Message)
		}
		if isLifecycleBoundaryKind(LifecycleBoundaryKind(entry.Kind)) {
			messages = append([]*schema.Message(nil), entry.Messages...)
		}
	}
	return messages
}

// LoadRefProjection reads the active transcript without resolving private
// media. Ref-backed prompts are represented as user messages containing only
// their text projection and are paired with PromptRecords.
func (r *Recorder) LoadRefProjection() (*LoadResult, error) {
	if r == nil {
		return &LoadResult{}, nil
	}
	result, _, err := loadTranscriptFileContextMode(
		context.Background(),
		r.Path(),
		false,
	)
	return result, err
}

func loadTranscriptFile(path string) (*LoadResult, []recordEntry, error) {
	return loadTranscriptFileContext(context.Background(), path)
}

func loadTranscriptFileContext(
	ctx context.Context,
	path string,
) (*LoadResult, []recordEntry, error) {
	return loadTranscriptFileContextMode(ctx, path, true)
}

func loadTranscriptFileContextMode(
	ctx context.Context,
	path string,
	resolveMedia bool,
) (*LoadResult, []recordEntry, error) {
	if path == "" {
		return &LoadResult{}, nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &LoadResult{}, nil, nil
		}
		return nil, nil, err
	}
	defer f.Close() //nolint:errcheck

	revisionHash := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(f, revisionHash))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	result := &LoadResult{
		Entries:                 make([]DurableEntry, 0),
		Messages:                make([]*schema.Message, 0),
		Replacements:            make([]Replacement, 0),
		Metadata:                make([]MetadataEntry, 0),
		FileSnapshots:           make([]map[string]FileState, 0),
		LifecycleBoundaries:     make([]LifecycleBoundary, 0),
		AgentCompletionReceipts: make([]AgentCompletionReceipt, 0),
		GoalUsageRecords:        make([]GoalUsageRecord, 0),
	}
	entries := make([]recordEntry, 0)
	entryLines := make([]int, 0)
	mediaMessages := make(map[*schema.Message]PromptRecordBinding)
	lineNum := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Skip lines that are clearly not JSON (null bytes, binary garbage).
		if !isLikelyJSON(line) {
			result.Corruptions = append(result.Corruptions, CorruptionInfo{
				Line:    lineNum,
				Err:     errors.New("line does not appear to be valid JSON"),
				RawLine: truncateRawLine(string(line)),
			})
			continue
		}

		var entry recordEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			if bytes.Contains(line, []byte(`"user_prompt"`)) ||
				bytes.Contains(line, []byte(`"prompt_messages"`)) ||
				durablePromptKindPattern.Match(line) {
				return nil, nil, &promptrecord.Error{
					Category: "malformed_record",
				}
			}
			// Record corruption but continue loading remaining lines.
			corruption := CorruptionInfo{
				Line:    lineNum,
				Err:     fmt.Errorf("json unmarshal: %w", err),
				RawLine: truncateRawLine(string(line)),
			}
			result.Corruptions = append(result.Corruptions, corruption)
			if bytes.Contains(line, []byte(GoalUsageRecordKind)) ||
				bytes.Contains(line, []byte(`"goal_usage"`)) {
				result.GoalUsageCorruptions = append(
					result.GoalUsageCorruptions,
					corruption,
				)
			}
			continue
		}
		if err := loadPromptRecord(
			ctx,
			path,
			&entry,
			resolveMedia,
		); err != nil {
			return nil, nil, err
		}
		entries = append(entries, entry)
		entryLines = append(entryLines, lineNum)
		if entry.UserPrompt != nil || len(entry.PromptMessages) > 0 {
			result.HasMediaRefs = true
			if entry.UserPrompt != nil && entry.Message != nil {
				binding := PromptRecordBinding{
					MessageIndex:  -1,
					Record:        entry.UserPrompt.Clone(),
					RuntimeItemID: entry.RuntimeItemID,
				}
				mediaMessages[entry.Message] = binding
				result.AllPromptRecords = append(
					result.AllPromptRecords,
					binding,
				)
			}
			for _, indexed := range entry.PromptMessages {
				if indexed.Index >= 0 &&
					indexed.Index < len(entry.Messages) &&
					entry.Messages[indexed.Index] != nil {
					binding := PromptRecordBinding{
						MessageIndex:  -1,
						Record:        indexed.Prompt.Clone(),
						RuntimeItemID: indexed.RuntimeItemID,
					}
					mediaMessages[entry.Messages[indexed.Index]] = binding
					result.AllPromptRecords = append(
						result.AllPromptRecords,
						binding,
					)
				}
			}
		}
		if entry.Message != nil {
			result.Messages = append(result.Messages, entry.Message)
			result.Usage.ObserveMessage(entry.Message)
			if receipt, ok := AgentCompletionReceiptFromMessage(entry.Message); ok {
				result.AgentCompletionReceipts = appendLoadedAgentCompletionReceipt(
					result.AgentCompletionReceipts,
					receipt,
				)
			}
		}
		if entry.Kind == "content-replacement" && len(entry.Replacements) > 0 {
			result.Replacements = append(result.Replacements, entry.Replacements...)
		}
		if entry.Kind == "metadata" && entry.MetaKey != "" {
			result.Metadata = append(result.Metadata, MetadataEntry{
				Key:       entry.MetaKey,
				Value:     entry.MetaValue,
				Timestamp: entry.Timestamp,
			})
		}
		if entry.Kind == "file-history-snapshot" && len(entry.FileStates) > 0 {
			result.FileSnapshots = append(result.FileSnapshots, entry.FileStates)
		}
		if entry.Kind == GoalUsageRecordKind {
			if entry.GoalUsage == nil {
				corruption := CorruptionInfo{
					Line: lineNum,
					Err:  errors.New("goal usage record has no payload"),
				}
				result.Corruptions = append(result.Corruptions, corruption)
				result.GoalUsageCorruptions = append(
					result.GoalUsageCorruptions,
					corruption,
				)
			} else {
				result.GoalUsageRecords = append(
					result.GoalUsageRecords,
					*entry.GoalUsage,
				)
			}
		}
		if kind := LifecycleBoundaryKind(entry.Kind); isLifecycleBoundaryKind(kind) {
			var usageSnapshot *UsageSummary
			if entry.Usage != nil {
				copied := normalizeUsageSnapshot(*entry.Usage)
				result.Usage = copied
				usageSnapshot = &copied
			} else if boundaryContainsUsageRelevantMessage(entry.Messages) {
				result.Usage.LegacyBoundariesWithoutUsage++
			}
			boundary := LifecycleBoundary{
				Timestamp:    entry.Timestamp,
				Kind:         kind,
				Messages:     cloneMessages(entry.Messages),
				Replacements: cloneReplacements(entry.Replacements),
				FileStates:   cloneFileStates(entry.FileStates),
				Usage:        usageSnapshot,
			}
			result.LifecycleBoundaries = append(result.LifecycleBoundaries, boundary)
			for _, message := range entry.Messages {
				if receipt, ok := AgentCompletionReceiptFromMessage(message); ok {
					result.AgentCompletionReceipts = appendLoadedAgentCompletionReceipt(
						result.AgentCompletionReceipts,
						receipt,
					)
				}
			}
			result.Messages = cloneMessages(entry.Messages)
			result.Replacements = cloneReplacements(entry.Replacements)
			result.FileSnapshots = result.FileSnapshots[:0]
			if len(entry.FileStates) > 0 {
				result.FileSnapshots = append(
					result.FileSnapshots,
					cloneFileStates(entry.FileStates),
				)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// Scanner errors (e.g., line too long) are recorded as corruption at the
		// end but we still return what we recovered so far.
		result.Corruptions = append(result.Corruptions, CorruptionInfo{
			Line: lineNum + 1,
			Err:  fmt.Errorf("scanner: %w", err),
		})
	}
	revisionComplete := true
	if _, err := io.Copy(revisionHash, f); err != nil {
		revisionComplete = false
		result.Corruptions = append(result.Corruptions, CorruptionInfo{
			Line: lineNum + 1,
			Err:  fmt.Errorf("revision fingerprint: %w", err),
		})
	}
	if revisionComplete {
		result.Revision = TranscriptRevision(fmt.Sprintf("sha256:%x", revisionHash.Sum(nil)))
	}
	for index, message := range result.Messages {
		if binding, ok := mediaMessages[message]; ok {
			result.MediaMessageIndexes = append(
				result.MediaMessageIndexes,
				index,
			)
			binding.MessageIndex = index
			result.PromptRecords = append(result.PromptRecords, binding)
		}
	}
	source := transcriptSourceIdentity(path)
	seenPersistedIdentities := make(map[string]struct{}, len(entries))
	for index := range entries {
		entry := entries[index]
		identity := EntryIdentity{}
		if entry.EntryID != nil && isPersistedEntryIdentity(*entry.EntryID) {
			key := entry.EntryID.Key()
			if _, duplicate := seenPersistedIdentities[key]; !duplicate {
				identity = *entry.EntryID
				seenPersistedIdentities[key] = struct{}{}
			} else {
				result.Corruptions = append(result.Corruptions, CorruptionInfo{
					Line: entryLines[index],
					Err:  fmt.Errorf("duplicate persisted transcript entry identity %q", key),
				})
				entries[index].EntryID = nil
			}
		} else if entry.EntryID != nil {
			result.Corruptions = append(result.Corruptions, CorruptionInfo{
				Line: entryLines[index],
				Err:  errors.New("invalid persisted transcript entry identity"),
			})
			entries[index].EntryID = nil
		}
		if !isPersistedEntryIdentity(identity) {
			identity, err = legacyTranscriptEntryIdentity(
				source,
				uint64(index),
				entry,
				result.Revision,
			)
			if err != nil {
				return nil, nil, err
			}
		}
		result.Entries = append(
			result.Entries,
			durableEntryFromRecord(uint64(index), entry, identity),
		)
	}
	return result, entries, nil
}

func loadPromptRecord(
	ctx context.Context,
	path string,
	entry *recordEntry,
	resolveMedia bool,
) error {
	if resolveMedia {
		return materializePromptRecord(ctx, path, entry)
	}
	return projectPromptRecord(entry)
}

func projectPromptRecord(entry *recordEntry) error {
	if entry == nil {
		return nil
	}
	if entry.Kind == promptrecord.Kind {
		if entry.UserPrompt == nil ||
			entry.Message != nil ||
			len(entry.Messages) != 0 ||
			len(entry.PromptMessages) != 0 ||
			entry.RuntimeItemID != "" &&
				!validRuntimeItemDeliveryID(entry.RuntimeItemID) {
			return &promptrecord.Error{Category: "invalid_record_envelope"}
		}
		message, err := promptRecordTextMessage(*entry.UserPrompt)
		if err != nil {
			return err
		}
		attachRuntimeItemDeliveryIdentity(message, entry.RuntimeItemID)
		entry.Message = message
		return nil
	}
	if entry.UserPrompt != nil || entry.RuntimeItemID != "" {
		return &promptrecord.Error{Category: "unexpected_prompt_payload"}
	}
	if len(entry.PromptMessages) == 0 {
		return nil
	}
	kind := LifecycleBoundaryKind(entry.Kind)
	if !isLifecycleBoundaryKind(kind) {
		return &promptrecord.Error{Category: "unexpected_prompt_snapshot"}
	}
	seen := make(map[int]struct{}, len(entry.PromptMessages))
	for _, indexed := range entry.PromptMessages {
		if indexed.Index < 0 ||
			indexed.Index >= len(entry.Messages) ||
			entry.Messages[indexed.Index] != nil {
			return &promptrecord.Error{Category: "invalid_prompt_snapshot_index"}
		}
		if _, exists := seen[indexed.Index]; exists {
			return &promptrecord.Error{Category: "duplicate_prompt_snapshot_index"}
		}
		seen[indexed.Index] = struct{}{}
		message, err := promptRecordTextMessage(indexed.Prompt)
		if err != nil {
			return err
		}
		attachRuntimeItemDeliveryIdentity(message, indexed.RuntimeItemID)
		entry.Messages[indexed.Index] = message
	}
	return nil
}

func promptRecordTextMessage(record promptrecord.Record) (*schema.Message, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	var content strings.Builder
	for _, part := range record.Parts {
		if part.Kind == promptrecord.PartText && part.Text != nil {
			content.WriteString(part.Text.Text)
		}
	}
	return &schema.Message{
		Role:    schema.User,
		Content: content.String(),
	}, nil
}

func materializePromptRecord(
	ctx context.Context,
	path string,
	entry *recordEntry,
) error {
	if entry == nil {
		return nil
	}
	if entry.Kind == promptrecord.Kind {
		if entry.UserPrompt == nil ||
			entry.Message != nil ||
			len(entry.Messages) != 0 ||
			len(entry.PromptMessages) != 0 ||
			entry.RuntimeItemID != "" &&
				!validRuntimeItemDeliveryID(entry.RuntimeItemID) {
			return &promptrecord.Error{Category: "invalid_record_envelope"}
		}
		message, err := entry.UserPrompt.Materialize(
			ctx,
			mediastore.New(path+".media"),
		)
		if err != nil {
			return err
		}
		attachRuntimeItemDeliveryIdentity(message, entry.RuntimeItemID)
		entry.Message = message
		return nil
	}
	if entry.UserPrompt != nil || entry.RuntimeItemID != "" {
		return &promptrecord.Error{Category: "unexpected_prompt_payload"}
	}
	if len(entry.PromptMessages) == 0 {
		return nil
	}
	kind := LifecycleBoundaryKind(entry.Kind)
	if !isLifecycleBoundaryKind(kind) {
		return &promptrecord.Error{Category: "unexpected_prompt_snapshot"}
	}
	seen := make(map[int]struct{}, len(entry.PromptMessages))
	for _, indexed := range entry.PromptMessages {
		if indexed.Index < 0 ||
			indexed.Index >= len(entry.Messages) ||
			entry.Messages[indexed.Index] != nil {
			return &promptrecord.Error{Category: "invalid_prompt_snapshot_index"}
		}
		if _, exists := seen[indexed.Index]; exists {
			return &promptrecord.Error{Category: "duplicate_prompt_snapshot_index"}
		}
		seen[indexed.Index] = struct{}{}
		message, err := indexed.Prompt.Materialize(
			ctx,
			mediastore.New(path+".media"),
		)
		if err != nil {
			return err
		}
		attachRuntimeItemDeliveryIdentity(
			message,
			indexed.RuntimeItemID,
		)
		entry.Messages[indexed.Index] = message
	}
	return nil
}

// RecordLifecycleBoundary appends and fsyncs one active-context transition.
// The successful return is the commit point; callers must mutate in-memory
// state only after this method succeeds.
func (r *Recorder) RecordLifecycleBoundary(
	kind LifecycleBoundaryKind,
	messages []*schema.Message,
	replacements []Replacement,
	fileStates map[string]FileState,
) error {
	return r.RecordLifecycleBoundaryWithUsage(
		kind,
		messages,
		replacements,
		fileStates,
		UsageSummary{},
		false,
	)
}

// RecordLifecycleBoundaryWithUsage appends one active-context transition and
// a cumulative provider-reported usage snapshot in the same durable record.
func (r *Recorder) RecordLifecycleBoundaryWithUsage(
	kind LifecycleBoundaryKind,
	messages []*schema.Message,
	replacements []Replacement,
	fileStates map[string]FileState,
	usage UsageSummary,
	usageKnown bool,
) error {
	if r == nil {
		return nil
	}
	if !isLifecycleBoundaryKind(kind) {
		return fmt.Errorf("unsupported lifecycle boundary kind %q", kind)
	}
	if r.Path() == "" {
		return errors.New("lifecycle boundary requires a transcript path")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureFileOpen(); err != nil {
		return err
	}
	if r.file == nil {
		return nil
	}
	var usageSnapshot *UsageSummary
	if usageKnown {
		copied := prepareUsageSnapshot(usage)
		usageSnapshot = &copied
	}
	promptMessages := r.promptMessageRecordsLocked(messages)
	if err := r.encodeEntryLocked(recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      string(kind),
		Messages:  cloneMessages(messages),
		assistantOriginSources: append(
			[]*schema.Message(nil),
			messages...,
		),
		PromptMessages: promptMessages,
		Replacements:   cloneReplacements(replacements),
		FileStates:     cloneFileStates(fileStates),
		Usage:          usageSnapshot,
	}, "encode lifecycle boundary"); err != nil {
		return err
	}
	if err := r.syncOpenFileAndParent(); err != nil {
		r.closeFileAfterDurabilityFailure()
		return err
	}
	return nil
}

func (r *Recorder) promptMessageRecordsLocked(
	messages []*schema.Message,
) []promptMessageRecord {
	if r == nil || len(messages) == 0 {
		return nil
	}
	records := make([]promptMessageRecord, 0)
	for index, message := range messages {
		record, ok := r.promptRecords[message]
		if !ok {
			continue
		}
		records = append(records, promptMessageRecord{
			Index:         index,
			Prompt:        record.Clone(),
			RuntimeItemID: runtimeItemDeliveryID(message),
		})
	}
	return records
}

func isLifecycleBoundaryKind(kind LifecycleBoundaryKind) bool {
	switch kind {
	case LifecycleSessionStart,
		LifecycleReset,
		LifecycleCompact,
		LifecycleCheckpoint:
		return true
	default:
		return false
	}
}

func cloneMessages(messages []*schema.Message) []*schema.Message {
	if len(messages) == 0 {
		return make([]*schema.Message, 0)
	}
	return append(make([]*schema.Message, 0, len(messages)), messages...)
}

func cloneReplacements(replacements []Replacement) []Replacement {
	if len(replacements) == 0 {
		return make([]Replacement, 0)
	}
	return append(make([]Replacement, 0, len(replacements)), replacements...)
}

func cloneFileStates(states map[string]FileState) map[string]FileState {
	if len(states) == 0 {
		return nil
	}
	cloned := make(map[string]FileState, len(states))
	for path, state := range states {
		cloned[path] = state
	}
	return cloned
}

// Replace rewrites the transcript with the provided messages.
func (r *Recorder) Replace(messages []*schema.Message) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.replaceLocked(messages, nil)
}

// Record appends messages to the transcript.
func (r *Recorder) Record(messages []*schema.Message, isAssistant bool) error {
	if r == nil || len(messages) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureFileOpen(); err != nil {
		return err
	}

	kind := "user"
	if isAssistant {
		kind = "assistant"
	}
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		entry := r.recordEntryForMessageLocked(msg)
		entry.Kind = kind
		if entry.UserPrompt != nil {
			entry.Kind = promptrecord.Kind
		}
		if err := r.encodeEntryLocked(entry, "encode transcript message"); err != nil {
			return err
		}
	}
	return nil
}

// RecordMessages appends messages using each message's own role. It is used by
// the engine's incremental checkpoint path so normal turns remain append-only
// without repeating the full active context at every checkpoint.
func (r *Recorder) RecordMessages(messages []*schema.Message) error {
	if r == nil || len(messages) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureFileOpen(); err != nil {
		return err
	}
	if r.file == nil {
		return nil
	}
	for _, message := range messages {
		if message == nil {
			continue
		}
		if err := r.encodeEntryLocked(
			r.recordEntryForMessageLocked(message),
			"encode transcript message",
		); err != nil {
			return err
		}
	}
	return nil
}

func (r *Recorder) recordEntryForMessageLocked(
	message *schema.Message,
) recordEntry {
	entry := recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      messageKind(message),
		Message:   message,
	}
	if record, ok := r.promptRecords[message]; ok {
		entry.Kind = promptrecord.Kind
		entry.UserPrompt = userPromptPointer(record)
		entry.RuntimeItemID = runtimeItemDeliveryID(message)
	}
	return entry
}

// RecordUserPrompt appends exactly one ref-backed ordered prompt. The caller
// must publish and sync every referenced media entry before invoking it.
func (r *Recorder) RecordUserPrompt(
	record promptrecord.Record,
	message *schema.Message,
) error {
	return r.recordUserPrompt(record, message, "")
}

// RecordRuntimeUserPrompt appends a ref-backed ordered prompt together with
// the exact runtime-input delivery identity used for crash settlement.
func (r *Recorder) RecordRuntimeUserPrompt(
	record promptrecord.Record,
	message *schema.Message,
	runtimeItemID string,
) error {
	runtimeItemID = strings.TrimSpace(runtimeItemID)
	if !validRuntimeItemDeliveryID(runtimeItemID) {
		return errors.New("durable user prompt has invalid runtime item identity")
	}
	if runtimeItemDeliveryID(message) != runtimeItemID {
		return errors.New("durable user prompt has mismatched runtime item identity")
	}
	return r.recordUserPrompt(record, message, runtimeItemID)
}

func (r *Recorder) recordUserPrompt(
	record promptrecord.Record,
	message *schema.Message,
	runtimeItemID string,
) error {
	if r == nil {
		return nil
	}
	if message == nil {
		return errors.New("durable user prompt requires a materialized message")
	}
	if err := record.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureFileOpen(); err != nil {
		return err
	}
	if r.file == nil {
		return nil
	}
	entry := recordEntry{
		Timestamp:     time.Now().UTC(),
		Kind:          promptrecord.Kind,
		Message:       message,
		UserPrompt:    userPromptPointer(record),
		RuntimeItemID: runtimeItemID,
	}
	if err := ensureTranscriptEntryIdentity(&entry); err != nil {
		return err
	}
	if err := r.encodeEntryLocked(entry, "encode durable user prompt"); err != nil {
		return err
	}
	return nil
}

func userPromptPointer(record promptrecord.Record) *promptrecord.Record {
	cloned := record.Clone()
	return &cloned
}

// RecordContentReplacements appends a content-replacement entry to the transcript.
// Mirrors reference sessionStorage.ts:insertContentReplacement.
func (r *Recorder) RecordContentReplacements(replacements []Replacement) error {
	if r == nil || len(replacements) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureFileOpen(); err != nil {
		return err
	}

	return r.encodeEntryLocked(recordEntry{
		Timestamp:    time.Now().UTC(),
		Kind:         "content-replacement",
		Replacements: replacements,
	}, "encode content replacements")
}

// ReplaceWithReplacements rewrites the transcript with messages plus content-replacement records.
// Used during checkpoint to preserve both conversation state and replacement decisions.
func (r *Recorder) ReplaceWithReplacements(messages []*schema.Message, replacements []Replacement) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.replaceLocked(messages, replacements)
}

func (r *Recorder) replaceLocked(messages []*schema.Message, replacements []Replacement) error {
	path := r.Path()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	rewrittenEntries, err := r.prepareRewriteEntriesLocked(messages, replacements)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath) //nolint:errcheck
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}

	enc := json.NewEncoder(temp)
	for index := range rewrittenEntries {
		if err := ensureTranscriptEntryIdentity(&rewrittenEntries[index]); err != nil {
			_ = temp.Close()
			return err
		}
		if err := r.attachAssistantOriginsLocked(&rewrittenEntries[index]); err != nil {
			_ = temp.Close()
			return err
		}
		if err := encodeNewTranscriptEntry(enc, rewrittenEntries[index]); err != nil {
			_ = temp.Close()
			return err
		}
	}

	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceTranscriptFile(tempPath, path); err != nil {
		return err
	}
	r.trackLoadedMessageIdentitiesLocked(rewrittenEntries)
	r.file, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	return err
}

func replaceTranscriptFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}

func messageKind(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	switch msg.Role {
	case schema.Assistant:
		return "assistant"
	case schema.Tool:
		return "tool"
	case schema.System:
		return "system"
	default:
		return "user"
	}
}

// Flush ensures all buffered writes are persisted.
func (r *Recorder) Flush() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	if err := r.syncOpenFileAndParent(); err != nil {
		r.closeFileAfterDurabilityFailure()
		return err
	}
	return nil
}

// Close flushes and releases the transcript file descriptor. Long-lived query
// engines may keep a Recorder open; short-lived launch persistence should close
// it explicitly.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	if err := r.syncOpenFileAndParent(); err != nil {
		r.closeFileAfterDurabilityFailure()
		return err
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// RecordMetadata appends a metadata key-value entry to the transcript.
// Used for recording session-level state (model, CWD, git-branch, custom-title, etc.).
// Mirrors the reference's metadata recording in session storage.
func (r *Recorder) RecordMetadata(key, value string) error {
	if r == nil || key == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureFileOpen(); err != nil {
		return err
	}

	return r.encodeEntryLocked(recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      "metadata",
		MetaKey:   key,
		MetaValue: value,
	}, "encode transcript metadata")
}

// RecordFileHistorySnapshot appends a file-history-snapshot entry to the transcript.
// Records which files have been read/edited/written at this point, enabling
// reconstruction of FileStateCache on resume.
func (r *Recorder) RecordFileHistorySnapshot(files map[string]FileState) error {
	if r == nil || len(files) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureFileOpen(); err != nil {
		return err
	}

	return r.encodeEntryLocked(recordEntry{
		Timestamp:  time.Now().UTC(),
		Kind:       "file-history-snapshot",
		FileStates: files,
	}, "encode file-history snapshot")
}

// ensureFileOpen opens the transcript file for writing if not already open.
// Must be called with r.mu held.
func (r *Recorder) ensureFileOpen() error {
	if r.file != nil {
		return nil
	}
	path := r.Path()
	if path == "" {
		return nil
	}
	if r.partialLineRepairPending {
		if err := truncatePartialJSONLLine(path); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return err
		}
		r.partialLineRepairPending = false
	}
	_, statErr := os.Stat(path)
	creating := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !creating {
		return statErr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	r.file = f
	r.parentDirSyncPending = r.parentDirSyncPending || creating
	return nil
}

func (r *Recorder) encodeEntryLocked(entry recordEntry, operation string) error {
	if r == nil || r.file == nil {
		return nil
	}
	if r.beforeEncode != nil {
		if err := r.beforeEncode(operation); err != nil {
			return fmt.Errorf("%s: %w", operation, err)
		}
	}
	if err := ensureTranscriptEntryIdentity(&entry); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if err := r.attachAssistantOriginsLocked(&entry); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if err := json.NewEncoder(r.file).Encode(entry.persistable()); err != nil {
		r.partialLineRepairPending = true
		r.closeFileAfterDurabilityFailure()
		return &DurabilityUncertainError{
			Operation: operation,
			Err:       err,
		}
	}
	r.trackMessageIdentityLocked(entry)
	return nil
}

func encodeNewTranscriptEntry(enc *json.Encoder, entry recordEntry) error {
	if err := ensureTranscriptEntryIdentity(&entry); err != nil {
		return err
	}
	return enc.Encode(entry.persistable())
}

func (e recordEntry) persistable() recordEntry {
	persisted := e
	if persisted.UserPrompt != nil {
		persisted.Message = nil
	}
	if len(persisted.PromptMessages) > 0 {
		persisted.Messages = cloneMessages(persisted.Messages)
		for _, indexed := range persisted.PromptMessages {
			if indexed.Index >= 0 && indexed.Index < len(persisted.Messages) {
				persisted.Messages[indexed.Index] = nil
			}
		}
	}
	return persisted
}

func truncatePartialJSONLLine(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	const blockSize int64 = 4096
	buffer := make([]byte, blockSize)
	for end := size; end > 0; {
		start := end - blockSize
		if start < 0 {
			start = 0
		}
		n, readErr := file.ReadAt(buffer[:end-start], start)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if index := bytes.LastIndexByte(buffer[:n], '\n'); index >= 0 {
			completeSize := start + int64(index) + 1
			if completeSize == size {
				return nil
			}
			return file.Truncate(completeSize)
		}
		end = start
	}
	return file.Truncate(0)
}

func (r *Recorder) syncOpenFileAndParent() error {
	if r == nil || r.file == nil {
		return nil
	}
	syncFile := r.syncFile
	if syncFile == nil {
		syncFile = func(file *os.File) error {
			return file.Sync()
		}
	}
	if err := syncFile(r.file); err != nil {
		return &DurabilityUncertainError{
			Operation: "sync transcript file",
			Err:       err,
		}
	}
	if !r.parentDirSyncPending {
		return nil
	}
	syncDir := r.syncDir
	if syncDir == nil {
		syncDir = syncDirectory
	}
	parent := filepath.Dir(r.Path())
	if err := syncDir(parent); err != nil {
		return &DurabilityUncertainError{
			Operation: "sync transcript directory",
			Err:       err,
		}
	}
	r.parentDirSyncPending = false
	return nil
}

func (r *Recorder) closeFileAfterDurabilityFailure() {
	if r == nil || r.file == nil {
		return
	}
	_ = r.file.Close()
	r.file = nil
}

func syncDirectory(path string) error {
	// Windows does not expose POSIX directory fsync through os.File.Sync. File
	// Sync remains the strongest portable durability boundary there.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close() //nolint:errcheck
	return dir.Sync()
}

// AtomicReplace rewrites the transcript atomically using a temp file + rename.
// This prevents corruption if the process crashes during write.
func (r *Recorder) AtomicReplace(messages []*schema.Message) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	path := r.Path()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// Close existing handle so rename works.
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	rewrittenEntries, err := r.prepareRewriteEntriesLocked(messages, nil)
	if err != nil {
		return err
	}

	// Write to temp file.
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}

	enc := json.NewEncoder(f)
	for index := range rewrittenEntries {
		if err := encodeNewTranscriptEntry(enc, rewrittenEntries[index]); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}

	// Fsync before rename ensures data is durable.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	r.trackLoadedMessageIdentitiesLocked(rewrittenEntries)

	return nil
}

// --- Corruption recovery helpers ---

// isLikelyJSON performs a cheap pre-filter to skip obviously non-JSON lines.
// Returns true if the line starts with '{' or '[' (possibly after whitespace).
// This avoids feeding binary garbage to the JSON parser.
func isLikelyJSON(line []byte) bool {
	for _, b := range line {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

// truncateRawLine returns the first N characters of a line for diagnostic
// purposes. Avoids storing large corrupt lines in memory.
func truncateRawLine(line string) string {
	const maxLen = 200
	if len(line) <= maxLen {
		return line
	}
	return line[:maxLen] + "..."
}

// Branch creates a new session transcript by copying the first `messageCount`
// messages from this session into a new session file. This implements session
// forking/branching: the new session starts with a prefix of the parent's
// conversation history.
//
// Returns a new Recorder for the branched session, or an error if the branch
// cannot be created. The parent session's parentID is recorded as metadata in
// the new transcript.
//
// This operation is atomic: it writes to a same-directory temp file, syncs it,
// and installs the target without replacing an existing child.
func (r *Recorder) Branch(newSessionID string, messageCount int) (*Recorder, error) {
	return r.BranchWithState(newSessionID, messageCount, BranchState{})
}

// BranchState carries active-context state and additional metadata that must be
// committed in the same child transcript as the copied message prefix.
type BranchState struct {
	Replacements  []Replacement
	FileSnapshots []map[string]FileState
	Metadata      []MetadataEntry
}

// BranchWithState creates one no-clobber child transcript containing the
// selected messages, active replacement/file state, lineage, and caller
// metadata. The target name becomes visible only after the temp file is synced.
func (r *Recorder) BranchWithState(
	newSessionID string,
	messageCount int,
	state BranchState,
) (*Recorder, error) {
	if r == nil {
		return nil, errors.New("cannot branch from nil recorder")
	}
	if newSessionID == "" {
		return nil, errors.New("new session ID must not be empty")
	}
	if messageCount < 0 {
		return nil, errors.New("messageCount must be non-negative")
	}

	// Load the source transcript (tolerant of corruption).
	result, err := r.LoadFull()
	if err != nil {
		return nil, fmt.Errorf("load source transcript: %w", err)
	}

	// Determine how many messages to copy.
	copyCount := messageCount
	if copyCount > len(result.Messages) {
		copyCount = len(result.Messages)
	}
	if copyCount == 0 {
		return nil, errors.New("no messages to branch from")
	}
	for _, index := range result.MediaMessageIndexes {
		if index < copyCount {
			return nil, ErrMediaBranchUnsupported
		}
	}

	messagesToCopy := make([]BranchMessage, 0, copyCount)
	for _, message := range result.Messages[:copyCount] {
		messagesToCopy = append(messagesToCopy, BranchMessage{Message: message})
	}
	return r.branchProjectionWithState(newSessionID, messagesToCopy, state)
}

// BranchMessage is one final active-context message selected for a child.
// PromptRecord is set only for a ref-backed user prompt.
type BranchMessage struct {
	Message       *schema.Message
	PromptRecord  *promptrecord.Record
	RuntimeItemID string
}

// BranchProjectionWithState commits one already-frozen active-message prefix.
// Callers own source revision validation and private media remapping.
func (r *Recorder) BranchProjectionWithState(
	newSessionID string,
	messages []BranchMessage,
	state BranchState,
) (*Recorder, error) {
	if r == nil {
		return nil, errors.New("cannot branch from nil recorder")
	}
	if newSessionID == "" {
		return nil, errors.New("new session ID must not be empty")
	}
	if len(messages) == 0 {
		return nil, errors.New("no messages to branch from")
	}
	for _, selected := range messages {
		if selected.Message == nil {
			return nil, errors.New("branch projection contains a nil message")
		}
		if selected.PromptRecord == nil {
			if selected.RuntimeItemID != "" {
				return nil, errors.New("branch projection has an unexpected runtime item identity")
			}
			continue
		}
		if err := selected.PromptRecord.Validate(); err != nil {
			return nil, err
		}
		if selected.RuntimeItemID != "" &&
			!validRuntimeItemDeliveryID(selected.RuntimeItemID) {
			return nil, errors.New("branch projection has an invalid runtime item identity")
		}
	}
	return r.branchProjectionWithState(newSessionID, messages, state)
}

func (r *Recorder) branchProjectionWithState(
	newSessionID string,
	messagesToCopy []BranchMessage,
	state BranchState,
) (*Recorder, error) {
	// Create the new recorder.
	newRec := NewRecorder(newSessionID, r.Dir)
	newPath := newRec.Path()
	if newPath == "" {
		return nil, errors.New("cannot determine path for new session")
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}

	// Write to a unique same-directory temp file. The platform commit helper
	// installs it without replacing an existing child.
	f, err := os.CreateTemp(
		filepath.Dir(newPath),
		"."+filepath.Base(newPath)+".tmp-*",
	)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("set temp file mode: %w", err)
	}

	enc := json.NewEncoder(f)

	// Write messages.
	for _, selected := range messagesToCopy {
		entry := recordEntry{
			Timestamp: time.Now().UTC(),
			Kind:      messageKind(selected.Message),
			Message:   selected.Message,
		}
		if selected.PromptRecord != nil {
			entry.Kind = promptrecord.Kind
			entry.UserPrompt = userPromptPointer(*selected.PromptRecord)
			entry.RuntimeItemID = selected.RuntimeItemID
		}
		if sourceOrigin, ok := r.AssistantOrigin(selected.Message); ok {
			newRec.stageDurableAssistantOrigin(selected.Message, sourceOrigin)
		}
		if err := ensureTranscriptEntryIdentity(&entry); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("assign branch entry identity: %w", err)
		}
		if err := newRec.attachAssistantOriginsLocked(&entry); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("bind branch assistant origin: %w", err)
		}
		if err := encodeNewTranscriptEntry(enc, entry); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("encode message: %w", err)
		}
	}

	if len(state.Replacements) > 0 {
		if err := encodeNewTranscriptEntry(enc, recordEntry{
			Timestamp:    time.Now().UTC(),
			Kind:         "content-replacement",
			Replacements: cloneReplacements(state.Replacements),
		}); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("encode replacements: %w", err)
		}
	}
	for _, snapshot := range state.FileSnapshots {
		if len(snapshot) == 0 {
			continue
		}
		if err := encodeNewTranscriptEntry(enc, recordEntry{
			Timestamp:  time.Now().UTC(),
			Kind:       "file-history-snapshot",
			FileStates: cloneFileStates(snapshot),
		}); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("encode file snapshot: %w", err)
		}
	}

	// Write parent session metadata to record lineage.
	if err := encodeNewTranscriptEntry(enc, recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      "metadata",
		MetaKey:   "parent_session_id",
		MetaValue: r.SessionID,
	}); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("encode parent metadata: %w", err)
	}

	// Write branch point metadata.
	if err := encodeNewTranscriptEntry(enc, recordEntry{
		Timestamp: time.Now().UTC(),
		Kind:      "metadata",
		MetaKey:   "branch_point",
		MetaValue: fmt.Sprintf("%d", len(messagesToCopy)),
	}); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("encode branch point metadata: %w", err)
	}
	for _, metadata := range state.Metadata {
		if metadata.Key == "" {
			continue
		}
		timestamp := metadata.Timestamp
		if timestamp.IsZero() {
			timestamp = time.Now().UTC()
		}
		if err := encodeNewTranscriptEntry(enc, recordEntry{
			Timestamp: timestamp,
			Kind:      "metadata",
			MetaKey:   metadata.Key,
			MetaValue: metadata.Value,
		}); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("encode fork metadata %s: %w", metadata.Key, err)
		}
	}

	syncFile := r.syncFile
	if syncFile == nil {
		syncFile = func(file *os.File) error { return file.Sync() }
	}
	if err := syncFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close: %w", err)
	}
	committed, err := commitNewTranscriptFile(tmpPath, newPath)
	if err != nil {
		if committed {
			return nil, &DurabilityUncertainError{
				Operation: "commit fork transcript",
				Err:       err,
			}
		}
		return nil, fmt.Errorf("commit fork transcript: %w", err)
	}
	syncDir := r.syncDir
	if syncDir == nil {
		syncDir = syncDirectory
	}
	if err := syncDir(filepath.Dir(newPath)); err != nil {
		return nil, &DurabilityUncertainError{
			Operation: "sync fork transcript directory",
			Err:       err,
		}
	}

	return newRec, nil
}
