package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/providerorigin"
)

const assistantOriginBindingCodec = "assistant-origin-binding/v1"

type assistantOriginRecord struct {
	BindingCodec  string                `json:"binding_codec"`
	EntryVersion  int                   `json:"entry_version"`
	EntryID       string                `json:"entry_id"`
	MessageIndex  int                   `json:"message_index"`
	LogicalID     string                `json:"logical_message_id"`
	PayloadDigest string                `json:"payload_digest"`
	Origin        providerorigin.Origin `json:"origin"`
}

type assistantOriginEnvelope struct {
	records   []assistantOriginRecord
	raw       json.RawMessage
	malformed bool
}

func newAssistantOriginEnvelope(
	records []assistantOriginRecord,
) *assistantOriginEnvelope {
	if len(records) == 0 {
		return nil
	}
	return &assistantOriginEnvelope{
		records: append([]assistantOriginRecord(nil), records...),
	}
}

func (e *assistantOriginEnvelope) MarshalJSON() ([]byte, error) {
	if e == nil {
		return []byte("null"), nil
	}
	if e.malformed && len(e.raw) > 0 {
		return bytes.Clone(e.raw), nil
	}
	return json.Marshal(e.records)
}

func (e *assistantOriginEnvelope) UnmarshalJSON(data []byte) error {
	if e == nil {
		return nil
	}
	e.raw = bytes.Clone(data)
	e.records = nil
	e.malformed = false
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		e.malformed = true
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var records []assistantOriginRecord
	if err := decoder.Decode(&records); err != nil {
		e.malformed = true
		return nil
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		e.malformed = true
		return nil
	}
	e.records = records
	return nil
}

func canonicalAssistantMessageBytes(message *schema.Message) ([]byte, error) {
	if message == nil {
		return nil, errors.New("assistant origin message is nil")
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode assistant origin message: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode assistant origin message: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("assistant origin message has trailing JSON")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize assistant origin message: %w", err)
	}
	return canonical, nil
}

func assistantOriginPayloadDigest(message *schema.Message) (string, error) {
	canonical, err := canonicalAssistantMessageBytes(message)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func assistantLogicalID(message *schema.Message) (string, error) {
	if message == nil || message.Role != schema.Assistant || message.Extra == nil {
		return "", errors.New("assistant origin requires a logical message ID")
	}
	logicalID, ok := message.Extra["message_id"].(string)
	if !ok || strings.TrimSpace(logicalID) == "" || len(logicalID) > 128 {
		return "", errors.New("assistant origin has an invalid logical message ID")
	}
	return logicalID, nil
}

func canonicalAssistantOriginDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (r *Recorder) StageAssistantOrigin(
	message *schema.Message,
	origin providerorigin.Origin,
) error {
	if r == nil {
		return nil
	}
	if !origin.DurableValid() || origin.RoutePublication == 0 {
		return errors.New("assistant origin is incomplete")
	}
	if _, err := assistantLogicalID(message); err != nil {
		return err
	}
	if _, err := assistantOriginPayloadDigest(message); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingAssistantOrigins == nil {
		r.pendingAssistantOrigins = make(
			map[*schema.Message]providerorigin.Origin,
		)
	}
	r.pendingAssistantOrigins[message] = origin
	return nil
}

func (r *Recorder) stageDurableAssistantOrigin(
	message *schema.Message,
	origin providerorigin.Origin,
) {
	if r == nil || message == nil || !origin.DurableValid() {
		return
	}
	if r.pendingAssistantOrigins == nil {
		r.pendingAssistantOrigins = make(
			map[*schema.Message]providerorigin.Origin,
		)
	}
	r.pendingAssistantOrigins[message] = origin
}

func (r *Recorder) attachAssistantOriginsLocked(entry *recordEntry) error {
	if r == nil || entry == nil || entry.EntryID == nil ||
		!isPersistedEntryIdentity(*entry.EntryID) {
		return nil
	}
	messages := entry.Messages
	if entry.Message != nil {
		messages = []*schema.Message{entry.Message}
	}
	records := make([]assistantOriginRecord, 0)
	for index, message := range messages {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		originSource := message
		if entry.Message != nil && entry.assistantOriginSource != nil {
			originSource = entry.assistantOriginSource
		} else if index < len(entry.assistantOriginSources) &&
			entry.assistantOriginSources[index] != nil {
			originSource = entry.assistantOriginSources[index]
		}
		if !assistantOriginMessagesEquivalent(originSource, message) {
			continue
		}
		origin, ok := r.lookupAssistantOriginLocked(originSource)
		if !ok {
			continue
		}
		logicalID, err := assistantLogicalID(message)
		if err != nil {
			return err
		}
		payloadDigest, err := assistantOriginPayloadDigest(message)
		if err != nil {
			return err
		}
		durableOrigin := origin
		durableOrigin.RoutePublication = 0
		records = append(records, assistantOriginRecord{
			BindingCodec:  assistantOriginBindingCodec,
			EntryVersion:  entry.EntryID.Version,
			EntryID:       entry.EntryID.ID,
			MessageIndex:  index,
			LogicalID:     logicalID,
			PayloadDigest: payloadDigest,
			Origin:        durableOrigin,
		})
	}
	entry.AssistantOrigins = newAssistantOriginEnvelope(records)
	return nil
}

func assistantOriginMessagesEquivalent(left, right *schema.Message) bool {
	leftID, leftErr := assistantLogicalID(left)
	rightID, rightErr := assistantLogicalID(right)
	if leftErr != nil || rightErr != nil || leftID != rightID {
		return false
	}
	leftDigest, leftErr := assistantOriginPayloadDigest(left)
	rightDigest, rightErr := assistantOriginPayloadDigest(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}

func (r *Recorder) lookupAssistantOriginLocked(
	message *schema.Message,
) (providerorigin.Origin, bool) {
	if origin, ok := r.pendingAssistantOrigins[message]; ok {
		return origin, true
	}
	if resolution, ok := r.assistantOrigins[message]; ok &&
		resolution.State == providerorigin.BindingVerified {
		return resolution.Origin, true
	}
	return providerorigin.Origin{}, false
}

func (r *Recorder) trackAssistantOriginsLocked(entry recordEntry) {
	if r == nil || entry.AssistantOrigins == nil {
		return
	}
	if r.assistantOrigins == nil {
		r.assistantOrigins = make(
			map[*schema.Message]providerorigin.BindingResolution,
		)
	}
	messages := entry.Messages
	if entry.Message != nil {
		messages = []*schema.Message{entry.Message}
	}
	markAll := func(state providerorigin.BindingState) {
		for _, message := range messages {
			if message != nil && message.Role == schema.Assistant {
				r.assistantOrigins[message] = providerorigin.BindingResolution{
					State: state,
				}
			}
		}
	}
	envelope := entry.AssistantOrigins
	if envelope.malformed || entry.EntryID == nil ||
		!isPersistedEntryIdentity(*entry.EntryID) {
		markAll(providerorigin.BindingLegacyUnverified)
		return
	}
	byIndex := make(map[int]assistantOriginRecord, len(envelope.records))
	invalidEnvelope := false
	for _, record := range envelope.records {
		if record.MessageIndex < 0 || record.MessageIndex >= len(messages) ||
			messages[record.MessageIndex] == nil ||
			messages[record.MessageIndex].Role != schema.Assistant {
			invalidEnvelope = true
			continue
		}
		if _, duplicate := byIndex[record.MessageIndex]; duplicate {
			invalidEnvelope = true
			continue
		}
		byIndex[record.MessageIndex] = record
	}
	if invalidEnvelope {
		markAll(providerorigin.BindingLegacyUnverified)
		return
	}
	for index, record := range byIndex {
		message := messages[index]
		resolution := validateAssistantOriginRecord(
			*entry.EntryID,
			index,
			message,
			record,
		)
		r.assistantOrigins[message] = resolution
		if resolution.State == providerorigin.BindingVerified {
			r.clearPendingAssistantOriginLocked(message)
		}
	}
}

func (r *Recorder) clearPendingAssistantOriginLocked(message *schema.Message) {
	logicalID, err := assistantLogicalID(message)
	if err != nil {
		return
	}
	digest, err := assistantOriginPayloadDigest(message)
	if err != nil {
		return
	}
	for candidate := range r.pendingAssistantOrigins {
		candidateID, candidateErr := assistantLogicalID(candidate)
		if candidateErr != nil || candidateID != logicalID {
			continue
		}
		candidateDigest, candidateErr := assistantOriginPayloadDigest(candidate)
		if candidateErr == nil && candidateDigest == digest {
			delete(r.pendingAssistantOrigins, candidate)
		}
	}
}

func validateAssistantOriginRecord(
	identity EntryIdentity,
	index int,
	message *schema.Message,
	record assistantOriginRecord,
) providerorigin.BindingResolution {
	if record.BindingCodec != assistantOriginBindingCodec ||
		record.Origin.Version != providerorigin.OriginVersion {
		return providerorigin.BindingResolution{
			State: providerorigin.BindingLegacyUnverified,
		}
	}
	if !record.Origin.DurableValid() ||
		record.EntryVersion != identity.Version ||
		record.EntryID != identity.ID ||
		record.MessageIndex != index ||
		!canonicalAssistantOriginDigest(record.PayloadDigest) {
		return providerorigin.BindingResolution{
			State: providerorigin.BindingRecoveryMismatch,
		}
	}
	logicalID, err := assistantLogicalID(message)
	if err != nil || logicalID != record.LogicalID {
		return providerorigin.BindingResolution{
			State: providerorigin.BindingRecoveryMismatch,
		}
	}
	payloadDigest, err := assistantOriginPayloadDigest(message)
	if err != nil || payloadDigest != record.PayloadDigest {
		return providerorigin.BindingResolution{
			State: providerorigin.BindingRecoveryMismatch,
		}
	}
	return providerorigin.BindingResolution{
		State:  providerorigin.BindingVerified,
		Origin: record.Origin,
	}
}

type assistantOriginResolver struct {
	byMessage   map[*schema.Message]assistantOriginSnapshot
	byLogicalID map[string]struct{}
}

type assistantOriginSnapshot struct {
	logicalID  string
	digest     string
	resolution providerorigin.BindingResolution
}

func (r *assistantOriginResolver) ResolveAssistantOrigin(
	message *schema.Message,
) providerorigin.BindingResolution {
	if r == nil || message == nil || message.Role != schema.Assistant {
		return providerorigin.BindingResolution{State: providerorigin.BindingAbsent}
	}
	logicalID, err := assistantLogicalID(message)
	if err != nil {
		return providerorigin.BindingResolution{State: providerorigin.BindingAbsent}
	}
	if snapshot, ok := r.byMessage[message]; ok {
		digest, digestErr := assistantOriginPayloadDigest(message)
		if digestErr != nil || snapshot.logicalID != logicalID ||
			snapshot.digest != digest {
			return providerorigin.BindingResolution{
				State: providerorigin.BindingRecoveryMismatch,
			}
		}
		return snapshot.resolution
	}
	if _, known := r.byLogicalID[logicalID]; known {
		return providerorigin.BindingResolution{State: providerorigin.BindingRecoveryMismatch}
	}
	return providerorigin.BindingResolution{State: providerorigin.BindingAbsent}
}

func (r *Recorder) AssistantOriginResolver() providerorigin.BindingResolver {
	resolver := &assistantOriginResolver{
		byMessage:   make(map[*schema.Message]assistantOriginSnapshot),
		byLogicalID: make(map[string]struct{}),
	}
	if r == nil {
		return resolver
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for message, resolution := range r.assistantOrigins {
		logicalID, err := assistantLogicalID(message)
		if err != nil {
			continue
		}
		digest, err := assistantOriginPayloadDigest(message)
		if err != nil {
			continue
		}
		resolver.byMessage[message] = assistantOriginSnapshot{
			logicalID:  logicalID,
			digest:     digest,
			resolution: resolution,
		}
		resolver.byLogicalID[logicalID] = struct{}{}
	}
	return resolver
}

func (r *Recorder) AssistantOrigin(
	message *schema.Message,
) (providerorigin.Origin, bool) {
	if r == nil || message == nil {
		return providerorigin.Origin{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	resolution, ok := r.assistantOrigins[message]
	if !ok || resolution.State != providerorigin.BindingVerified {
		return providerorigin.Origin{}, false
	}
	return resolution.Origin, true
}

func (r *Recorder) RebindAssistantOrigins(
	source []*schema.Message,
	target []*schema.Message,
) error {
	if r == nil || len(source) == 0 || len(target) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebindAssistantOriginsLocked(source, target)
}

func (r *Recorder) rebindAssistantOriginsLocked(
	source []*schema.Message,
	target []*schema.Message,
) error {
	type candidate struct {
		logicalID  string
		digest     string
		resolution providerorigin.BindingResolution
		identities []trackedMessageIdentity
	}
	candidates := make([]candidate, 0)
	for _, message := range source {
		resolution, ok := r.assistantOrigins[message]
		if !ok {
			continue
		}
		logicalID, err := assistantLogicalID(message)
		if err != nil {
			continue
		}
		digest, err := assistantOriginPayloadDigest(message)
		if err != nil {
			return err
		}
		candidates = append(candidates, candidate{
			logicalID:  logicalID,
			digest:     digest,
			resolution: resolution,
			identities: append(
				[]trackedMessageIdentity(nil),
				r.messageIdentities[message]...,
			),
		})
	}
	used := make(map[int]struct{})
	for _, message := range target {
		logicalID, err := assistantLogicalID(message)
		if err != nil {
			continue
		}
		digest, err := assistantOriginPayloadDigest(message)
		if err != nil {
			return err
		}
		for index, current := range candidates {
			if _, exists := used[index]; exists ||
				current.logicalID != logicalID || current.digest != digest {
				continue
			}
			r.assistantOrigins[message] = current.resolution
			if len(current.identities) > 0 {
				r.messageIdentities[message] = append(
					r.messageIdentities[message],
					current.identities...,
				)
			}
			used[index] = struct{}{}
			break
		}
	}
	return nil
}
