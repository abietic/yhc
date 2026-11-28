package tools

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"slices"
	"time"
	"unicode/utf8"
)

const maxReadPageSpans = 128

// Line spans are zero-based and half-open, like Read's offset and limit.
type readLineSpan struct{ start, end int }

type readPageOwner struct{ session, thread, agent string }

func readPageOwnerFromContext(ctx context.Context) readPageOwner {
	if ctx == nil {
		return readPageOwner{}
	}
	return readPageOwner{SessionIDFromCtx(ctx), ThreadIDFromCtx(ctx), AgentIDFromCtx(ctx)}
}

type readPageCoverage struct {
	sha256 string
	owner  readPageOwner
	spans  []readLineSpan
}

func canRecordReadPage(ctx context.Context, size int, lines []string) bool {
	// Use the existing bounded verification path for complete paged reads.
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	if size > maxReadGuardFileBytes || (BoundFileReadGuards(ctx) == nil && SessionIDFromCtx(ctx) == "") {
		return false
	}
	for _, line := range lines {
		if len(line) > maxCharsPerLine || !utf8.ValidString(line) {
			return false
		}
	}
	return true
}

// recordFileReadPage promotes only complete coverage of the same observed bytes
// in the same session. Pending spans stay in this process and guard owner.
func recordFileReadPage(ctx context.Context, path string, data []byte, span readLineSpan, total int) bool {
	guards := fileReadGuardsFromContext(ctx)
	owner := readPageOwnerFromContext(ctx)
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	guards.Lock()
	defer guards.Unlock()

	previous := guards.m[path]
	entry := ReadFileEntry{IsPartial: true, sha256: digest, size: int64(len(data))}
	spans := []readLineSpan{span}
	if previous != nil {
		if !previous.IsPartial {
			if (!previous.paged && !guards.restored) ||
				(previous.sha256 == digest && (!previous.paged || previous.pageOwner == owner)) {
				return false
			}
			// Keep the earlier complete observation while collecting the new version.
			entry = *previous
		}
		if pages := previous.pages; pages != nil && pages.sha256 == digest && pages.owner == owner {
			spans = mergeReadPageSpans(append(spans, pages.spans...))
		}
	}
	entry.Timestamp = time.Now().UnixMilli()
	complete := len(spans) == 1 && spans[0] == (readLineSpan{0, total})
	if complete {
		entry = ReadFileEntry{Timestamp: entry.Timestamp, sha256: digest, size: int64(len(data)), paged: true, pageOwner: owner}
	} else {
		if len(spans) > maxReadPageSpans {
			spans = []readLineSpan{span}
		}
		entry.pages = &readPageCoverage{sha256: digest, owner: owner, spans: spans}
	}
	guards.m[path] = &entry
	guards.revision++
	return complete
}

func mergeReadPageSpans(spans []readLineSpan) []readLineSpan {
	slices.SortFunc(spans, func(a, b readLineSpan) int { return cmp.Compare(a.start, b.start) })
	merged := spans[:1]
	for _, span := range spans[1:] {
		last := &merged[len(merged)-1]
		if span.start <= last.end {
			last.end = max(last.end, span.end)
		} else {
			merged = append(merged, span)
		}
	}
	return merged
}
