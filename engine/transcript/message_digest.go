package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

// digestMessage retains the complete SDK message encoding, except that the
// newly introduced zero CacheWriteTokens has the pre-v0.9.21 representation.
// This keeps persisted v1 origin bindings and revision-scoped legacy entry
// identities stable. Nonzero counts and all Extra metadata remain bound.
// The persisted message itself is never changed.
type digestMessage schema.Message

func (m *digestMessage) MarshalJSON() ([]byte, error) {
	message := (*schema.Message)(m)
	raw, err := json.Marshal(message)
	if err != nil || message == nil || message.ResponseMeta == nil ||
		message.ResponseMeta.Usage == nil ||
		message.ResponseMeta.Usage.PromptTokenDetails.CacheWriteTokens != 0 {
		return raw, err
	}
	return normalizeDigestCacheWrite(raw, []string{"response_meta", "usage", "prompt_token_details"})
}

// Preserve object order outside the usage details: legacy entry digests hash
// the original struct encoding, while origin bindings sort the complete JSON.
func normalizeDigestCacheWrite(raw []byte, path []string) ([]byte, error) {
	if len(path) == 0 {
		var details map[string]json.RawMessage
		if err := json.Unmarshal(raw, &details); err != nil {
			return nil, err
		}
		delete(details, "cache_write_tokens")
		return json.Marshal(details)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("message digest expected an object at %s", path[0])
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var child json.RawMessage
		if err := decoder.Decode(&child); err != nil {
			return nil, err
		}
		if key != path[0] {
			continue
		}
		replacement, err := normalizeDigestCacheWrite(child, path[1:])
		if err != nil {
			return nil, err
		}
		end := int(decoder.InputOffset())
		out := append(bytes.Clone(raw[:end-len(child)]), replacement...)
		return append(out, raw[end:]...), nil
	}
	return raw, nil
}

func digestMessages(messages []*schema.Message) []*digestMessage {
	if messages == nil {
		return nil
	}
	out := make([]*digestMessage, len(messages))
	for i, message := range messages {
		out[i] = (*digestMessage)(message)
	}
	return out
}
