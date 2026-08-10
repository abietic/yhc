package transcript

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/promptrecord"
)

const (
	maxMessagePageLimit     = 128
	defaultMessagePageBytes = int64(2 * 1024 * 1024)
	maxMessagePageBytes     = int64(8 * 1024 * 1024)
	messagePageReadChunk    = int64(4 * 1024)
)

var (
	// ErrTranscriptPageRecordTooLarge reports that one physical JSONL record
	// cannot fit in the selector's bounded reverse-read budget.
	ErrTranscriptPageRecordTooLarge = errors.New("transcript page record exceeds bounded read budget")
	// ErrTranscriptPageCursorInvalid reports a malformed internal page boundary.
	ErrTranscriptPageCursorInvalid = errors.New("transcript page cursor is invalid")
	// ErrTranscriptEntryIdentityConflict reports the same persisted record ID
	// on different physical records. Returning the page would make dedup unsafe.
	ErrTranscriptEntryIdentityConflict = errors.New("transcript entry identity conflict")
	// ErrTranscriptRichPagingUnsupported is retained for compatibility with
	// P30.2a callers. P30.2c pages validated ref-backed prompt records.
	ErrTranscriptRichPagingUnsupported = errors.New("ref-backed transcript paging is unsupported")
)

// MessageEntryIdentity names one logical message within a physical durable
// transcript record. Lifecycle snapshots can contain several messages.
type MessageEntryIdentity struct {
	Record EntryIdentity
	Index  int
}

// Key returns the exact identity used for durable/live row matching.
func (i MessageEntryIdentity) Key() string {
	return fmt.Sprintf("%s/message/%d", i.Record.Key(), i.Index)
}

// MessagePageBoundary is an engine-internal physical continuation boundary.
// It is stored behind an opaque QueryEngine cursor rather than exposed to UI
// callers. BeforeOffset is used for ordinary reverse scanning. A non-negative
// LifecycleOffset continues within one active-context snapshot record.
type MessagePageBoundary struct {
	BeforeOffset       int64
	LifecycleOffset    int64
	LifecycleEnd       int64
	MessageBefore      int
	RecordOrdinal      uint64
	RecordOrdinalKnown bool
	ValidRecordsBefore uint64
	OrdinalKnown       bool
	LegacyRevision     TranscriptRevision
}

// MessagePageRequest describes one bounded reverse page over a frozen file
// prefix. ExpectedFile and SnapshotSize bind follow-up requests to the file
// object and exact initial prefix; appends are ignored, replacement/truncation
// fails closed.
type MessagePageRequest struct {
	Path         string
	Limit        int
	MaxBytes     int64
	SnapshotSize int64
	Boundary     MessagePageBoundary
	ExpectedFile os.FileInfo
}

// MessagePageEntry is one active-context message in transcript source order.
type MessagePageEntry struct {
	Identity      MessageEntryIdentity
	Timestamp     time.Time
	Kind          string
	Message       *schema.Message
	PromptRecord  *promptrecord.Record
	RuntimeItemID string
	RecordOffset  int64
}

// MessagePageResult is one bounded active-context page. FileInfo and Next are
// retained only by the QueryEngine cursor cache.
type MessagePageResult struct {
	Entries            []MessagePageEntry
	Next               MessagePageBoundary
	HasMore            bool
	BytesRead          int64
	CompatibilityBytes int64
	Corruptions        int
	SnapshotSize       int64
	FileInfo           os.FileInfo
}

type selectedMessageRecord struct {
	entry         recordEntry
	message       *schema.Message
	promptRecord  *promptrecord.Record
	runtimeItemID string
	messageIndex  int
	recordOffset  int64
	ordinal       uint64
	ordinalKnown  bool
}

// LoadMessagePage reads the newest active-context messages backwards and
// returns them in source order. Modern v1 records require only bounded reverse
// IO. A legacy record triggers a memory-bounded prefix scan to recover the
// P14.2a exact revision and valid-record ordinal without rewriting the file.
func LoadMessagePage(request MessagePageRequest) (*MessagePageResult, error) {
	path := strings.TrimSpace(request.Path)
	if path == "" {
		return nil, errors.New("transcript path is required")
	}
	limit, maxBytes, err := normalizeMessagePageRequest(request.Limit, request.MaxBytes)
	if err != nil {
		return nil, err
	}
	file, info, snapshotSize, boundary, err := openMessagePageSnapshot(request, path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck

	result := &MessagePageResult{
		Entries:      make([]MessagePageEntry, 0, limit),
		SnapshotSize: snapshotSize,
		FileInfo:     info,
	}
	if limit == 0 {
		result.HasMore = snapshotSize > 0
		result.Next = boundary
		return result, nil
	}

	selected := make([]selectedMessageRecord, 0, limit)
	if boundary.LifecycleOffset >= 0 {
		if err := selectLifecyclePage(file, boundary, limit, maxBytes, result, &selected); err != nil {
			return nil, err
		}
	} else {
		if err := selectReverseMessagePage(file, boundary, limit, maxBytes, result, &selected); err != nil {
			return nil, err
		}
	}
	if len(selected) == 0 && !result.HasMore {
		return result, nil
	}

	legacyOffsets := make(map[int64]struct{})
	for _, selectedRecord := range selected {
		if selectedRecord.entry.EntryID == nil ||
			!isPersistedEntryIdentity(*selectedRecord.entry.EntryID) {
			legacyOffsets[selectedRecord.recordOffset] = struct{}{}
		}
	}
	if boundary.LegacyRevision != "" || len(legacyOffsets) > 0 {
		analysis, analyzeErr := analyzeMessagePageSnapshot(
			file,
			snapshotSize,
			legacyOffsets,
			result.Next.BeforeOffset,
		)
		if analyzeErr != nil {
			return nil, analyzeErr
		}
		result.CompatibilityBytes = snapshotSize
		if boundary.LegacyRevision != "" && boundary.LegacyRevision != analysis.revision {
			return nil, fmt.Errorf(
				"%w: cursor=%q current=%q",
				ErrTranscriptRevisionChanged,
				boundary.LegacyRevision,
				analysis.revision,
			)
		}
		result.Next.LegacyRevision = analysis.revision
		result.Next.ValidRecordsBefore = analysis.validBefore
		result.Next.OrdinalKnown = true
		if result.Next.LifecycleOffset >= 0 && !result.Next.RecordOrdinalKnown {
			ordinal, ok := analysis.ordinals[result.Next.LifecycleOffset]
			if !ok {
				return nil, fmt.Errorf("%w: lifecycle record ordinal is unavailable", ErrTranscriptPageCursorInvalid)
			}
			result.Next.RecordOrdinal = ordinal
			result.Next.RecordOrdinalKnown = true
		}
		for index := range selected {
			if selected[index].ordinalKnown {
				continue
			}
			ordinal, ok := analysis.ordinals[selected[index].recordOffset]
			if !ok {
				return nil, fmt.Errorf("%w: message record ordinal is unavailable", ErrTranscriptPageCursorInvalid)
			}
			selected[index].ordinal = ordinal
			selected[index].ordinalKnown = true
		}
	}

	seenRecordOffsets := make(map[string]int64, len(selected))
	for index := len(selected) - 1; index >= 0; index-- {
		selectedRecord := selected[index]
		identity, identityErr := messagePageRecordIdentity(
			path,
			selectedRecord,
			result.Next.LegacyRevision,
		)
		if identityErr != nil {
			return nil, identityErr
		}
		key := identity.Key()
		if offset, exists := seenRecordOffsets[key]; exists && offset != selectedRecord.recordOffset {
			return nil, fmt.Errorf("%w: %s", ErrTranscriptEntryIdentityConflict, key)
		}
		seenRecordOffsets[key] = selectedRecord.recordOffset
		result.Entries = append(result.Entries, MessagePageEntry{
			Identity: MessageEntryIdentity{
				Record: identity,
				Index:  selectedRecord.messageIndex,
			},
			Timestamp:     selectedRecord.entry.Timestamp,
			Kind:          selectedRecord.entry.Kind,
			Message:       selectedRecord.message,
			PromptRecord:  selectedRecord.promptRecord,
			RuntimeItemID: selectedRecord.runtimeItemID,
			RecordOffset:  selectedRecord.recordOffset,
		})
	}
	return result, nil
}

func normalizeMessagePageRequest(limit int, maxBytes int64) (int, int64, error) {
	if limit < 0 {
		return 0, 0, errors.New("transcript page limit cannot be negative")
	}
	if limit > maxMessagePageLimit {
		limit = maxMessagePageLimit
	}
	if maxBytes <= 0 {
		maxBytes = defaultMessagePageBytes
	} else if maxBytes > maxMessagePageBytes {
		maxBytes = maxMessagePageBytes
	}
	return limit, maxBytes, nil
}

func openMessagePageSnapshot(
	request MessagePageRequest,
	path string,
) (*os.File, os.FileInfo, int64, MessagePageBoundary, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, 0, MessagePageBoundary{}, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return nil, nil, 0, MessagePageBoundary{}, errors.New("transcript page requires a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, MessagePageBoundary{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, 0, MessagePageBoundary{}, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(linkInfo, info) {
		_ = file.Close()
		return nil, nil, 0, MessagePageBoundary{}, fmt.Errorf("%w: transcript file changed while opening", ErrTranscriptRevisionChanged)
	}
	if request.ExpectedFile == nil {
		snapshotSize := info.Size()
		return file, info, snapshotSize, MessagePageBoundary{
			BeforeOffset:    snapshotSize,
			LifecycleOffset: -1,
			MessageBefore:   -1,
		}, nil
	}
	if !os.SameFile(request.ExpectedFile, info) || info.Size() < request.SnapshotSize {
		_ = file.Close()
		return nil, nil, 0, MessagePageBoundary{}, fmt.Errorf("%w: transcript was replaced or truncated", ErrTranscriptRevisionChanged)
	}
	if err := validateMessagePageBoundary(request.Boundary, request.SnapshotSize); err != nil {
		_ = file.Close()
		return nil, nil, 0, MessagePageBoundary{}, err
	}
	return file, info, request.SnapshotSize, request.Boundary, nil
}

func validateMessagePageBoundary(boundary MessagePageBoundary, snapshotSize int64) error {
	if snapshotSize < 0 || boundary.BeforeOffset < 0 || boundary.BeforeOffset > snapshotSize {
		return fmt.Errorf("%w: offset outside snapshot", ErrTranscriptPageCursorInvalid)
	}
	if boundary.LifecycleOffset < 0 {
		if boundary.MessageBefore > 0 || boundary.LifecycleEnd != 0 {
			return fmt.Errorf("%w: unexpected lifecycle continuation", ErrTranscriptPageCursorInvalid)
		}
		return nil
	}
	if boundary.LifecycleOffset >= boundary.LifecycleEnd ||
		boundary.LifecycleEnd > snapshotSize ||
		boundary.MessageBefore <= 0 {
		return fmt.Errorf("%w: malformed lifecycle continuation", ErrTranscriptPageCursorInvalid)
	}
	return nil
}

func selectLifecyclePage(
	file *os.File,
	boundary MessagePageBoundary,
	limit int,
	maxBytes int64,
	result *MessagePageResult,
	selected *[]selectedMessageRecord,
) error {
	lineSize := boundary.LifecycleEnd - boundary.LifecycleOffset
	if lineSize > maxBytes {
		return fmt.Errorf("%w: lifecycle record needs %d bytes", ErrTranscriptPageRecordTooLarge, lineSize)
	}
	line := make([]byte, lineSize)
	if _, err := file.ReadAt(line, boundary.LifecycleOffset); err != nil {
		return fmt.Errorf("read lifecycle transcript record: %w", err)
	}
	result.BytesRead = lineSize
	entry, err := decodeMessagePageRecord(line)
	if err != nil || !isLifecycleBoundaryKind(LifecycleBoundaryKind(entry.Kind)) {
		return fmt.Errorf("%w: lifecycle record no longer decodes", ErrTranscriptRevisionChanged)
	}
	if entryContainsMediaRefs(entry) {
		if err := projectPromptRecord(&entry); err != nil {
			return err
		}
	}
	if boundary.MessageBefore > len(entry.Messages) {
		return fmt.Errorf("%w: lifecycle message boundary changed", ErrTranscriptRevisionChanged)
	}
	messageBefore := boundary.MessageBefore
	for messageBefore > 0 && len(*selected) < limit {
		messageBefore--
		message := entry.Messages[messageBefore]
		if message == nil {
			continue
		}
		prompt, runtimeItemID := promptRecordAt(entry, messageBefore)
		*selected = append(*selected, selectedMessageRecord{
			entry:         entry,
			message:       message,
			promptRecord:  prompt,
			runtimeItemID: runtimeItemID,
			messageIndex:  messageBefore,
			recordOffset:  boundary.LifecycleOffset,
			ordinal:       boundary.RecordOrdinal,
			ordinalKnown:  boundary.RecordOrdinalKnown,
		})
	}
	if messageBefore > 0 {
		result.HasMore = true
		result.Next = boundary
		result.Next.MessageBefore = messageBefore
	} else {
		result.Next = MessagePageBoundary{LifecycleOffset: -1, MessageBefore: -1}
	}
	return nil
}

func selectReverseMessagePage(
	file *os.File,
	boundary MessagePageBoundary,
	limit int,
	maxBytes int64,
	result *MessagePageResult,
	selected *[]selectedMessageRecord,
) error {
	before := boundary.BeforeOffset
	validBefore := boundary.ValidRecordsBefore
	ordinalKnown := boundary.OrdinalKnown
	for before > 0 && len(*selected) < limit {
		line, start, end, bytesRead, complete, err := readTranscriptLineBefore(
			file,
			before,
			maxBytes-result.BytesRead,
		)
		result.BytesRead += bytesRead
		if err != nil {
			return err
		}
		if !complete {
			if len(*selected) == 0 {
				return fmt.Errorf("%w: record before offset %d", ErrTranscriptPageRecordTooLarge, before)
			}
			result.HasMore = true
			result.Next = boundary
			result.Next.BeforeOffset = before
			result.Next.ValidRecordsBefore = validBefore
			result.Next.OrdinalKnown = ordinalKnown
			return nil
		}
		before = start
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, decodeErr := decodeMessagePageRecord(line)
		if decodeErr != nil {
			if bytes.Contains(line, []byte(`"user_prompt"`)) ||
				bytes.Contains(line, []byte(`"prompt_messages"`)) ||
				durablePromptKindPattern.Match(line) {
				return &promptrecord.Error{Category: "malformed_record"}
			}
			result.Corruptions++
			continue
		}
		if entryContainsMediaRefs(entry) {
			if err := projectPromptRecord(&entry); err != nil {
				return err
			}
		}
		ordinal := uint64(0)
		if ordinalKnown {
			if validBefore == 0 {
				return fmt.Errorf("%w: valid-record ordinal underflow", ErrTranscriptPageCursorInvalid)
			}
			validBefore--
			ordinal = validBefore
		}
		if kind := LifecycleBoundaryKind(entry.Kind); isLifecycleBoundaryKind(kind) {
			messageBefore := len(entry.Messages)
			for messageBefore > 0 && len(*selected) < limit {
				messageBefore--
				message := entry.Messages[messageBefore]
				if message == nil {
					continue
				}
				prompt, runtimeItemID := promptRecordAt(entry, messageBefore)
				*selected = append(*selected, selectedMessageRecord{
					entry:         entry,
					message:       message,
					promptRecord:  prompt,
					runtimeItemID: runtimeItemID,
					messageIndex:  messageBefore,
					recordOffset:  start,
					ordinal:       ordinal,
					ordinalKnown:  ordinalKnown,
				})
			}
			if messageBefore > 0 {
				result.HasMore = true
				result.Next = MessagePageBoundary{
					BeforeOffset:       start,
					LifecycleOffset:    start,
					LifecycleEnd:       end,
					MessageBefore:      messageBefore,
					RecordOrdinal:      ordinal,
					RecordOrdinalKnown: ordinalKnown,
					ValidRecordsBefore: validBefore,
					OrdinalKnown:       ordinalKnown,
					LegacyRevision:     boundary.LegacyRevision,
				}
			} else {
				result.Next = MessagePageBoundary{LifecycleOffset: -1, MessageBefore: -1}
			}
			return nil
		}
		if entry.Message != nil {
			prompt, runtimeItemID := promptRecordAt(entry, 0)
			*selected = append(*selected, selectedMessageRecord{
				entry:         entry,
				message:       entry.Message,
				promptRecord:  prompt,
				runtimeItemID: runtimeItemID,
				messageIndex:  0,
				recordOffset:  start,
				ordinal:       ordinal,
				ordinalKnown:  ordinalKnown,
			})
		}
	}
	result.Next = MessagePageBoundary{
		BeforeOffset:       before,
		LifecycleOffset:    -1,
		MessageBefore:      -1,
		ValidRecordsBefore: validBefore,
		OrdinalKnown:       ordinalKnown,
		LegacyRevision:     boundary.LegacyRevision,
	}
	result.HasMore = len(*selected) == limit && before > 0
	return nil
}

func entryContainsMediaRefs(entry recordEntry) bool {
	return entry.UserPrompt != nil || len(entry.PromptMessages) > 0
}

func promptRecordAt(
	entry recordEntry,
	messageIndex int,
) (*promptrecord.Record, string) {
	if messageIndex == 0 && entry.UserPrompt != nil {
		record := entry.UserPrompt.Clone()
		return &record, entry.RuntimeItemID
	}
	for _, indexed := range entry.PromptMessages {
		if indexed.Index != messageIndex {
			continue
		}
		record := indexed.Prompt.Clone()
		return &record, indexed.RuntimeItemID
	}
	return nil, ""
}

func readTranscriptLineBefore(
	file *os.File,
	before int64,
	budget int64,
) ([]byte, int64, int64, int64, bool, error) {
	if before <= 0 {
		return nil, 0, 0, 0, true, nil
	}
	if budget <= 0 {
		return nil, before, before, 0, false, nil
	}
	contentEnd := before
	last := []byte{0}
	if _, err := file.ReadAt(last, contentEnd-1); err != nil {
		return nil, 0, 0, 0, false, err
	}
	bytesRead := int64(1)
	parts := make([][]byte, 0, 2)
	if last[0] == '\n' {
		contentEnd--
	} else {
		parts = append(parts, []byte{last[0]})
	}
	position := contentEnd
	if last[0] != '\n' {
		position--
	}
	for position > 0 {
		remaining := budget - bytesRead
		if remaining <= 0 {
			return nil, before, contentEnd, bytesRead, false, nil
		}
		readSize := min(messagePageReadChunk, position, remaining)
		start := position - readSize
		chunk := make([]byte, readSize)
		n, err := file.ReadAt(chunk, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, 0, 0, bytesRead, false, err
		}
		chunk = chunk[:n]
		bytesRead += int64(n)
		if newline := bytes.LastIndexByte(chunk, '\n'); newline >= 0 {
			parts = append(parts, append([]byte(nil), chunk[newline+1:]...))
			line := joinReverseLineParts(parts)
			line = bytes.TrimSuffix(line, []byte{'\r'})
			return line, start + int64(newline) + 1, contentEnd, bytesRead, true, nil
		}
		parts = append(parts, append([]byte(nil), chunk...))
		position = start
	}
	line := joinReverseLineParts(parts)
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return line, 0, contentEnd, bytesRead, true, nil
}

func joinReverseLineParts(parts [][]byte) []byte {
	total := 0
	for _, part := range parts {
		total += len(part)
	}
	joined := make([]byte, 0, total)
	for index := len(parts) - 1; index >= 0; index-- {
		joined = append(joined, parts[index]...)
	}
	return joined
}

func decodeMessagePageRecord(line []byte) (recordEntry, error) {
	if !isLikelyJSON(line) {
		return recordEntry{}, errors.New("line does not appear to be valid JSON")
	}
	var entry recordEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return recordEntry{}, err
	}
	return entry, nil
}

type messagePageSnapshotAnalysis struct {
	revision    TranscriptRevision
	ordinals    map[int64]uint64
	validBefore uint64
}

func analyzeMessagePageSnapshot(
	file *os.File,
	snapshotSize int64,
	targetOffsets map[int64]struct{},
	beforeOffset int64,
) (messagePageSnapshotAnalysis, error) {
	section := io.NewSectionReader(file, 0, snapshotSize)
	hash := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(section, hash))
	scanner.Buffer(make([]byte, 0, 64*1024), int(maxMessagePageBytes))
	scanner.Split(splitPhysicalJSONLLines)
	analysis := messagePageSnapshotAnalysis{
		ordinals: make(map[int64]uint64, len(targetOffsets)),
	}
	var offset int64
	var ordinal uint64
	for scanner.Scan() {
		line := scanner.Bytes()
		lineStart := offset
		offset += int64(len(line))
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, err := decodeMessagePageRecord(line)
		if err != nil {
			continue
		}
		if _, wanted := targetOffsets[lineStart]; wanted {
			analysis.ordinals[lineStart] = ordinal
		}
		if lineStart < beforeOffset {
			analysis.validBefore++
		}
		ordinal++
		_ = entry
	}
	if err := scanner.Err(); err != nil {
		return messagePageSnapshotAnalysis{}, fmt.Errorf("%w: legacy compatibility scan: %w", ErrTranscriptPageRecordTooLarge, err)
	}
	analysis.revision = TranscriptRevision(fmt.Sprintf("sha256:%x", hash.Sum(nil)))
	return analysis, nil
}

func splitPhysicalJSONLLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
		return newline + 1, data[:newline+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func messagePageRecordIdentity(
	path string,
	selected selectedMessageRecord,
	revision TranscriptRevision,
) (EntryIdentity, error) {
	if selected.entry.EntryID != nil && isPersistedEntryIdentity(*selected.entry.EntryID) {
		return *selected.entry.EntryID, nil
	}
	if !selected.ordinalKnown || revision == "" {
		return EntryIdentity{}, fmt.Errorf("%w: legacy identity context is unavailable", ErrTranscriptPageCursorInvalid)
	}
	return legacyTranscriptEntryIdentity(
		transcriptSourceIdentity(path),
		selected.ordinal,
		selected.entry,
		revision,
	)
}
