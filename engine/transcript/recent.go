package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	defaultRecentReadBytes int64 = 512 * 1024
	maxRecentReadBytes     int64 = 2 * 1024 * 1024
)

// RecentResult is a bounded tail projection for picker previews.
type RecentResult struct {
	Messages    []*schema.Message
	BytesRead   int64
	Truncated   bool
	Corruptions int
}

// LoadRecent reads at most maxBytes from the transcript tail and returns the
// newest limit message entries in chronological order. It never scans the full
// file merely to render a picker preview.
func LoadRecent(path string, limit int, maxBytes int64) (*RecentResult, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("transcript path is required")
	}
	if limit <= 0 {
		limit = 4
	}
	if maxBytes <= 0 {
		maxBytes = defaultRecentReadBytes
	} else if maxBytes > maxRecentReadBytes {
		maxBytes = maxRecentReadBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	readBytes := min(stat.Size(), maxBytes)
	offset := stat.Size() - readBytes
	buffer := make([]byte, readBytes)
	if readBytes > 0 {
		if _, err := file.ReadAt(buffer, offset); err != nil {
			return nil, fmt.Errorf("read transcript tail: %w", err)
		}
	}
	text := string(buffer)
	if offset > 0 {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = text[newline+1:]
		} else {
			text = ""
		}
	}
	lines := strings.Split(text, "\n")
	messages := make([]*schema.Message, 0, limit)
	corruptions := 0
	for index := len(lines) - 1; index >= 0 && len(messages) < limit; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		var entry recordEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			corruptions++
			continue
		}
		if entry.Message == nil {
			continue
		}
		messages = append(messages, entry.Message)
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return &RecentResult{
		Messages:    messages,
		BytesRead:   readBytes,
		Truncated:   offset > 0,
		Corruptions: corruptions,
	}, nil
}
