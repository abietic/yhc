package transcript

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/internal/providerorigin"
	"github.com/cloudwego/eino/schema"
)

const (
	// TranscriptEntryIdentityVersion is the current durable entry identity codec.
	TranscriptEntryIdentityVersion = 1
	entryIdentityBytes             = 16
)

// ErrTranscriptRevisionChanged reports that a revision-scoped legacy cursor
// no longer names the transcript revision from which it was created.
var ErrTranscriptRevisionChanged = errors.New("transcript revision changed")

// TranscriptRevision fingerprints the exact bytes of one opened transcript.
// It is intentionally opaque to callers.
type TranscriptRevision string

// EntryIdentity names one physical durable transcript record. New records use
// a persisted Version/ID pair. Legacy records derive a fallback identity that
// is valid only while Revision remains unchanged.
type EntryIdentity struct {
	Version  int                `json:"version"`
	ID       string             `json:"id"`
	Legacy   bool               `json:"-"`
	Revision TranscriptRevision `json:"-"`
}

// IsLegacy reports whether the identity is a revision-scoped fallback rather
// than a persisted record identity.
func (i EntryIdentity) IsLegacy() bool {
	return i.Legacy
}

// Key returns a comparison key suitable for live/durable deduplication. A
// legacy key includes its revision so it can never compare equal after a
// transcript rewrite.
func (i EntryIdentity) Key() string {
	if i.Legacy {
		return fmt.Sprintf("legacy/%s/%s", i.Revision, i.ID)
	}
	return fmt.Sprintf("v%d/%s", i.Version, i.ID)
}

// ValidateEntryCursorRevision validates the revision component of a legacy
// identity. Persisted identities remain stable across transcript revisions.
func ValidateEntryCursorRevision(
	identity EntryIdentity,
	current TranscriptRevision,
) error {
	if !identity.Legacy {
		return nil
	}
	if identity.Revision == "" ||
		current == "" ||
		identity.Revision != current {
		return fmt.Errorf(
			"%w: cursor=%q current=%q",
			ErrTranscriptRevisionChanged,
			identity.Revision,
			current,
		)
	}
	return nil
}

// DurableEntry is one valid physical JSONL record in source order. It exposes
// the full record payload so later bounded selectors can project messages
// without reconstructing identity from display text.
type DurableEntry struct {
	Ordinal      uint64
	Identity     EntryIdentity
	Timestamp    time.Time
	Kind         string
	Message      *schema.Message
	Messages     []*schema.Message
	Replacements []Replacement
	MetaKey      string
	MetaValue    string
	FileStates   map[string]FileState
	Usage        *UsageSummary
	GoalUsage    *GoalUsageRecord
	HasMediaRefs bool
}

type trackedMessageIdentity struct {
	Identity  EntryIdentity
	Index     int
	Timestamp time.Time
	Digest    string
}

func newTranscriptEntryIdentity() (EntryIdentity, error) {
	buffer := make([]byte, entryIdentityBytes)
	if _, err := rand.Read(buffer); err != nil {
		return EntryIdentity{}, fmt.Errorf("generate transcript entry identity: %w", err)
	}
	return EntryIdentity{
		Version: TranscriptEntryIdentityVersion,
		ID:      hex.EncodeToString(buffer),
	}, nil
}

func isPersistedEntryIdentity(identity EntryIdentity) bool {
	return !identity.Legacy && identity.Version > 0 && strings.TrimSpace(identity.ID) != ""
}

func ensureTranscriptEntryIdentity(entry *recordEntry) error {
	if entry == nil || (entry.EntryID != nil && isPersistedEntryIdentity(*entry.EntryID)) {
		return nil
	}
	identity, err := newTranscriptEntryIdentity()
	if err != nil {
		return err
	}
	entry.EntryID = &identity
	return nil
}

func transcriptSourceIdentity(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func legacyTranscriptEntryIdentity(
	source string,
	ordinal uint64,
	entry recordEntry,
	revision TranscriptRevision,
) (EntryIdentity, error) {
	payloadDigest, err := transcriptEntryPayloadDigest(entry)
	if err != nil {
		return EntryIdentity{}, err
	}
	material, err := json.Marshal(struct {
		Source        string    `json:"source"`
		Ordinal       uint64    `json:"ordinal"`
		Timestamp     time.Time `json:"timestamp"`
		Kind          string    `json:"kind"`
		PayloadDigest string    `json:"payload_digest"`
	}{
		Source:        source,
		Ordinal:       ordinal,
		Timestamp:     entry.Timestamp,
		Kind:          entry.Kind,
		PayloadDigest: payloadDigest,
	})
	if err != nil {
		return EntryIdentity{}, fmt.Errorf("encode legacy transcript identity: %w", err)
	}
	digest := sha256.Sum256(material)
	return EntryIdentity{
		ID:       "legacy-" + hex.EncodeToString(digest[:]),
		Legacy:   true,
		Revision: revision,
	}, nil
}

func transcriptEntryPayloadDigest(entry recordEntry) (string, error) {
	persisted := entry.persistable()
	payload, err := json.Marshal(struct {
		Message          *schema.Message          `json:"message,omitempty"`
		Messages         []*schema.Message        `json:"messages,omitempty"`
		UserPrompt       *promptrecord.Record     `json:"user_prompt,omitempty"`
		PromptMessages   []promptMessageRecord    `json:"prompt_messages,omitempty"`
		Replacements     []Replacement            `json:"replacements,omitempty"`
		MetaKey          string                   `json:"meta_key,omitempty"`
		MetaValue        string                   `json:"meta_value,omitempty"`
		FileStates       map[string]FileState     `json:"file_states,omitempty"`
		Usage            *UsageSummary            `json:"usage,omitempty"`
		GoalUsage        *GoalUsageRecord         `json:"goal_usage,omitempty"`
		AssistantOrigins *assistantOriginEnvelope `json:"assistant_origins,omitempty"`
	}{
		Message:          persisted.Message,
		Messages:         persisted.Messages,
		UserPrompt:       persisted.UserPrompt,
		PromptMessages:   persisted.PromptMessages,
		Replacements:     persisted.Replacements,
		MetaKey:          persisted.MetaKey,
		MetaValue:        persisted.MetaValue,
		FileStates:       persisted.FileStates,
		Usage:            persisted.Usage,
		GoalUsage:        persisted.GoalUsage,
		AssistantOrigins: persisted.AssistantOrigins,
	})
	if err != nil {
		return "", fmt.Errorf("encode transcript entry payload: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func transcriptMessageDigest(kind string, message *schema.Message) (string, error) {
	payload, err := json.Marshal(struct {
		Kind    string          `json:"kind"`
		Message *schema.Message `json:"message"`
	}{
		Kind:    kind,
		Message: message,
	})
	if err != nil {
		return "", fmt.Errorf("encode transcript message payload: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func durableEntryFromRecord(
	ordinal uint64,
	entry recordEntry,
	identity EntryIdentity,
) DurableEntry {
	var usage *UsageSummary
	if entry.Usage != nil {
		copied := *entry.Usage
		usage = &copied
	}
	var goalUsage *GoalUsageRecord
	if entry.GoalUsage != nil {
		copied := *entry.GoalUsage
		goalUsage = &copied
	}
	return DurableEntry{
		Ordinal:      ordinal,
		Identity:     identity,
		Timestamp:    entry.Timestamp,
		Kind:         entry.Kind,
		Message:      entry.Message,
		Messages:     cloneMessages(entry.Messages),
		Replacements: cloneReplacements(entry.Replacements),
		MetaKey:      entry.MetaKey,
		MetaValue:    entry.MetaValue,
		FileStates:   cloneFileStates(entry.FileStates),
		Usage:        usage,
		GoalUsage:    goalUsage,
		HasMediaRefs: entry.UserPrompt != nil || len(entry.PromptMessages) > 0,
	}
}

func (r *Recorder) trackLoadedMessageIdentitiesLocked(entries []recordEntry) {
	if r == nil {
		return
	}
	r.messageIdentities = make(map[*schema.Message][]trackedMessageIdentity)
	r.promptRecords = make(map[*schema.Message]promptrecord.Record)
	r.assistantOrigins = make(map[*schema.Message]providerorigin.BindingResolution)
	r.pendingAssistantOrigins = make(map[*schema.Message]providerorigin.Origin)
	for _, entry := range entries {
		r.trackMessageIdentityLocked(entry)
	}
}

func (r *Recorder) trackMessageIdentityLocked(entry recordEntry) {
	if r == nil ||
		entry.EntryID == nil ||
		!isPersistedEntryIdentity(*entry.EntryID) {
		return
	}
	if entry.Message != nil {
		if entry.UserPrompt != nil {
			r.trackPromptRecordLocked(entry.Message, *entry.UserPrompt)
		}
		r.trackOneMessageIdentityLocked(entry, entry.Message, 0)
	}
	for _, indexed := range entry.PromptMessages {
		if indexed.Index >= 0 && indexed.Index < len(entry.Messages) {
			r.trackPromptRecordLocked(
				entry.Messages[indexed.Index],
				indexed.Prompt,
			)
		}
	}
	for index, message := range entry.Messages {
		if message != nil {
			r.trackOneMessageIdentityLocked(entry, message, index)
		}
	}
	r.trackAssistantOriginsLocked(entry)
}

func (r *Recorder) trackPromptRecordLocked(
	message *schema.Message,
	record promptrecord.Record,
) {
	if r == nil || message == nil {
		return
	}
	if r.promptRecords == nil {
		r.promptRecords = make(map[*schema.Message]promptrecord.Record)
	}
	r.promptRecords[message] = record.Clone()
}

func (r *Recorder) trackOneMessageIdentityLocked(
	entry recordEntry,
	message *schema.Message,
	index int,
) {
	digest, err := transcriptMessageDigest(entry.Kind, message)
	if err != nil {
		return
	}
	if r.messageIdentities == nil {
		r.messageIdentities = make(map[*schema.Message][]trackedMessageIdentity)
	}
	r.messageIdentities[message] = append(
		r.messageIdentities[message],
		trackedMessageIdentity{
			Identity:  *entry.EntryID,
			Index:     index,
			Timestamp: entry.Timestamp,
			Digest:    digest,
		},
	)
}

// LatestMessageEntryIdentity returns the newest successfully encoded physical
// transcript identity for the exact in-memory message pointer. It never
// derives identity from display content and never reports a legacy fallback.
func (r *Recorder) LatestMessageEntryIdentity(message *schema.Message) (MessageEntryIdentity, bool) {
	if r == nil || message == nil {
		return MessageEntryIdentity{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tracked := r.messageIdentities[message]
	if len(tracked) == 0 {
		return MessageEntryIdentity{}, false
	}
	latest := tracked[len(tracked)-1]
	return MessageEntryIdentity{Record: latest.Identity, Index: latest.Index}, true
}

// RebindPromptRecords transfers durable prompt ownership from one equivalent
// in-memory projection to another. Resume repair may clone schema messages;
// without this explicit handoff, a later lifecycle checkpoint could serialize
// the clone as legacy inline media.
func (r *Recorder) RebindPromptRecords(
	source []*schema.Message,
	target []*schema.Message,
) error {
	if r == nil || len(source) == 0 || len(target) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	type candidate struct {
		digest     string
		record     promptrecord.Record
		identities []trackedMessageIdentity
	}
	candidates := make([]candidate, 0)
	for _, message := range source {
		record, ok := r.promptRecords[message]
		if !ok {
			continue
		}
		digest, err := transcriptMessageDigest(
			messageKind(message),
			message,
		)
		if err != nil {
			return err
		}
		candidates = append(candidates, candidate{
			digest: digest,
			record: record.Clone(),
			identities: append(
				[]trackedMessageIdentity(nil),
				r.messageIdentities[message]...,
			),
		})
	}
	candidateIndex := 0
	for _, message := range target {
		if message == nil {
			continue
		}
		if _, alreadyBound := r.promptRecords[message]; alreadyBound {
			continue
		}
		digest, err := transcriptMessageDigest(
			messageKind(message),
			message,
		)
		if err != nil {
			return err
		}
		match := -1
		for index := candidateIndex; index < len(candidates); index++ {
			if candidates[index].digest == digest {
				match = index
				break
			}
		}
		if match < 0 {
			continue
		}
		current := candidates[match]
		candidateIndex = match + 1
		r.trackPromptRecordLocked(message, current.record)
		if len(current.identities) > 0 {
			r.messageIdentities[message] = append(
				r.messageIdentities[message],
				current.identities...,
			)
		}
	}
	return nil
}

type rewriteMessageCandidate struct {
	entry  recordEntry
	digest string
}

func (r *Recorder) prepareRewriteEntriesLocked(
	messages []*schema.Message,
	replacements []Replacement,
) ([]recordEntry, error) {
	_, existing, err := loadTranscriptFile(r.Path())
	if err != nil {
		return nil, fmt.Errorf("load transcript identities before rewrite: %w", err)
	}
	candidates := make([]rewriteMessageCandidate, 0, len(existing))
	candidateByKey := make(map[string]rewriteMessageCandidate)
	for _, entry := range existing {
		if entry.Message == nil ||
			entry.EntryID == nil ||
			!isPersistedEntryIdentity(*entry.EntryID) {
			continue
		}
		digest, digestErr := transcriptMessageDigest(entry.Kind, entry.Message)
		if digestErr != nil {
			return nil, digestErr
		}
		candidate := rewriteMessageCandidate{entry: entry, digest: digest}
		candidates = append(candidates, candidate)
		candidateByKey[entry.EntryID.Key()] = candidate
	}

	exact := make(map[int]rewriteMessageCandidate)
	reserved := make(map[string]struct{})
	for index, message := range messages {
		if message == nil {
			continue
		}
		digest, digestErr := transcriptMessageDigest(messageKind(message), message)
		if digestErr != nil {
			return nil, digestErr
		}
		for _, tracked := range r.messageIdentities[message] {
			if !isPersistedEntryIdentity(tracked.Identity) {
				continue
			}
			key := tracked.Identity.Key()
			if _, alreadyReserved := reserved[key]; alreadyReserved {
				continue
			}
			candidate, ok := candidateByKey[key]
			if !ok || digest != tracked.Digest || digest != candidate.digest {
				continue
			}
			exact[index] = candidate
			reserved[key] = struct{}{}
			break
		}
	}

	rewritten := make([]recordEntry, 0, len(messages)+1)
	used := make(map[string]struct{})
	fallbackIndex := 0
	for index, message := range messages {
		if message == nil {
			continue
		}
		kind := messageKind(message)
		digest, digestErr := transcriptMessageDigest(kind, message)
		if digestErr != nil {
			return nil, digestErr
		}
		candidate, matched := exact[index]
		if !matched && !isSynthesizedCompactionMessage(message) {
			for candidateIndex := fallbackIndex; candidateIndex < len(candidates); candidateIndex++ {
				current := candidates[candidateIndex]
				key := current.entry.EntryID.Key()
				if _, isReserved := reserved[key]; isReserved {
					continue
				}
				if _, isUsed := used[key]; isUsed || current.digest != digest {
					continue
				}
				candidate = current
				matched = true
				fallbackIndex = candidateIndex + 1
				break
			}
		}
		entry := recordEntry{
			Timestamp: time.Now().UTC(),
			Kind:      kind,
			Message:   message,
		}
		if record, ok := r.promptRecords[message]; ok {
			entry.Kind = promptrecord.Kind
			entry.UserPrompt = userPromptPointer(record)
		}
		if matched {
			identity := *candidate.entry.EntryID
			entry.EntryID = &identity
			entry.Timestamp = candidate.entry.Timestamp
			used[identity.Key()] = struct{}{}
		}
		rewritten = append(rewritten, entry)
	}
	rewritten = interleaveAuxiliaryUsageEntries(rewritten, existing)

	if len(replacements) > 0 {
		replacementEntry := recordEntry{
			Timestamp:    time.Now().UTC(),
			Kind:         "content-replacement",
			Replacements: cloneReplacements(replacements),
		}
		rewritten = append(rewritten, replacementEntry)
	}
	return rewritten, nil
}

func interleaveAuxiliaryUsageEntries(
	rewritten []recordEntry,
	existing []recordEntry,
) []recordEntry {
	positions := make(map[string]int, len(rewritten))
	for index := range rewritten {
		entry := rewritten[index]
		if entry.EntryID != nil && isPersistedEntryIdentity(*entry.EntryID) {
			positions[entry.EntryID.Key()] = index
		}
	}
	buckets := make([][]recordEntry, len(rewritten)+1)
	count := 0
	for index := range existing {
		entry := existing[index]
		if entry.Kind != AuxiliaryUsageRecordKind || entry.Usage == nil ||
			entry.Usage.Version != UsageSummaryVersion {
			continue
		}
		position := len(rewritten)
		for next := index + 1; next < len(existing); next++ {
			identity := existing[next].EntryID
			if identity == nil || !isPersistedEntryIdentity(*identity) {
				continue
			}
			if candidate, ok := positions[identity.Key()]; ok {
				position = candidate
				break
			}
		}
		copied := entry
		usage := *entry.Usage
		copied.Usage = &usage
		if entry.EntryID != nil {
			identity := *entry.EntryID
			copied.EntryID = &identity
		}
		buckets[position] = append(buckets[position], copied)
		count++
	}
	if count == 0 {
		return rewritten
	}
	result := make([]recordEntry, 0, len(rewritten)+count)
	for index := range rewritten {
		result = append(result, buckets[index]...)
		result = append(result, rewritten[index])
	}
	result = append(result, buckets[len(rewritten)]...)
	return result
}

func isSynthesizedCompactionMessage(message *schema.Message) bool {
	if message == nil || message.Extra == nil {
		return false
	}
	subtype, _ := message.Extra["subtype"].(string)
	return subtype == "compact_boundary" || subtype == "compact_summary"
}
