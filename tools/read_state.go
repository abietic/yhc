package tools

import (
	"sync"
	"time"
)

// ReadFileEntry records when a file was read and whether it was a partial view.
type ReadFileEntry struct {
	Timestamp int64 // Unix milliseconds
	IsPartial bool  // true until the read observation covers the file
	sha256    string
	size      int64
	paged     bool
	pageOwner readPageOwner
	pages     *readPageCoverage
}

// readFileState tracks which files have been read by the Read tool.
// Used by Edit and Write tools to enforce file-not-read guards.
type FileReadGuards struct {
	sync.RWMutex
	m        map[string]*ReadFileEntry
	root     string
	restored bool
	revision uint64
}

var readFileState = NewFileReadGuards()

// RecordFileRead records that a file was read at the current time.
func RecordFileRead(path string, isPartial bool) {
	readFileState.Lock()
	defer readFileState.Unlock()
	// A narrower reread does not erase knowledge from a prior complete read,
	// write, or edit. Keep partial-only files guarded until a complete read.
	entry := ReadFileEntry{IsPartial: isPartial}
	if previous := readFileState.m[path]; isPartial && previous != nil && !previous.IsPartial {
		entry = *previous
	}
	entry.Timestamp = time.Now().UnixMilli()
	readFileState.m[path] = &entry
}

// GetFileReadState returns the read state for a file, or nil if not read.
func GetFileReadState(path string) *ReadFileEntry {
	readFileState.RLock()
	defer readFileState.RUnlock()
	return readFileState.m[path]
}

// HasFileBeenRead reports the compatibility guard's complete unscoped reads.
func HasFileBeenRead(path string) bool {
	entry := GetFileReadState(path)
	return entry != nil && !entry.IsPartial && !entry.paged
}

// ResetFileReadState clears all read state (used in testing).
func ResetFileReadState() {
	readFileState.Lock()
	defer readFileState.Unlock()
	readFileState.m = make(map[string]*ReadFileEntry)
}
