package tools

import (
	"sync"
	"time"
)

// ReadFileEntry records when a file was read and whether it was a partial view.
type ReadFileEntry struct {
	Timestamp int64 // Unix milliseconds
	IsPartial bool  // true if offset/limit was used (partial view)
}

// readFileState tracks which files have been read by the Read tool.
// Used by Edit and Write tools to enforce file-not-read guards.
var readFileState = struct {
	sync.RWMutex
	m map[string]*ReadFileEntry
}{m: make(map[string]*ReadFileEntry)}

// RecordFileRead records that a file was read at the current time.
func RecordFileRead(path string, isPartial bool) {
	readFileState.Lock()
	defer readFileState.Unlock()
	readFileState.m[path] = &ReadFileEntry{
		Timestamp: time.Now().UnixMilli(),
		IsPartial: isPartial,
	}
}

// GetFileReadState returns the read state for a file, or nil if not read.
func GetFileReadState(path string) *ReadFileEntry {
	readFileState.RLock()
	defer readFileState.RUnlock()
	return readFileState.m[path]
}

// HasFileBeenRead returns true if the file has been fully read (not partial).
func HasFileBeenRead(path string) bool {
	entry := GetFileReadState(path)
	return entry != nil && !entry.IsPartial
}

// ResetFileReadState clears all read state (used in testing).
func ResetFileReadState() {
	readFileState.Lock()
	defer readFileState.Unlock()
	readFileState.m = make(map[string]*ReadFileEntry)
}
