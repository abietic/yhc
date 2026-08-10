package promptrecord

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/internal/mediastore"
)

func TestRecordRejectsUnknownTrailingAndDuplicateMedia(t *testing.T) {
	mediaID := strings.Repeat("a", 43)
	ref := mediastore.Ref{
		Version:   mediastore.RefVersion,
		MediaID:   mediaID,
		MIMEType:  "image/png",
		SizeBytes: 1,
		Width:     1,
		Height:    1,
	}
	duplicate := Record{
		Version: Version1,
		TurnID:  "turn-1",
		Parts: []Part{
			{
				Kind: PartImage,
				Image: &ImagePart{
					Ref:    ref,
					Detail: "auto",
				},
			},
			{
				Kind: PartImage,
				Image: &ImagePart{
					Ref:    ref,
					Detail: "high",
				},
			},
		},
	}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate media identity accepted")
	}

	valid, err := json.Marshal(Record{
		Version: Version1,
		TurnID:  "turn-1",
		Parts: []Part{{
			Kind: PartImage,
			Image: &ImagePart{
				Ref:    ref,
				Detail: "auto",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range [][]byte{
		append(append([]byte(nil), valid...), []byte(`{}`)...),
		[]byte(`{"version":1,"turn_id":"turn-1","parts":[],"unknown":true}`),
	} {
		var decoded Record
		if err := json.Unmarshal(content, &decoded); err == nil {
			t.Fatalf("strict decode accepted %s", content)
		}
	}
}
