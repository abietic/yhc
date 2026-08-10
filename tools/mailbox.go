package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MailboxMessage represents a message in a teammate's mailbox.
//
// Reference: src/utils/teammateMailbox.ts (~200 lines)
type MailboxMessage struct {
	From      string `json:"from"`
	Text      string `json:"text"`
	Summary   string `json:"summary,omitempty"`
	Timestamp string `json:"timestamp"`
	Color     string `json:"color,omitempty"`
}

var (
	mailboxDir string
	mailboxMu  sync.Mutex
)

// SetMailboxDir sets the directory for teammate mailbox files.
func SetMailboxDir(dir string) {
	mailboxMu.Lock()
	defer mailboxMu.Unlock()
	mailboxDir = dir
}

// WriteToMailbox writes a message to a teammate's mailbox file.
func WriteToMailbox(recipientName string, msg MailboxMessage, teamName string) error {
	mailboxMu.Lock()
	dir := mailboxDir
	mailboxMu.Unlock()

	if dir == "" {
		dir = filepath.Join(os.TempDir(), "eino-agent-mailbox")
	}

	if teamName != "" {
		dir = filepath.Join(dir, teamName)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mailbox: mkdir %s: %w", dir, err)
	}

	path := filepath.Join(dir, strings.ToLower(recipientName)+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("mailbox: open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}

// ReadMailbox reads and clears all messages from a teammate's mailbox.
func ReadMailbox(recipientName, teamName string) ([]MailboxMessage, error) {
	mailboxMu.Lock()
	dir := mailboxDir
	mailboxMu.Unlock()

	if dir == "" {
		dir = filepath.Join(os.TempDir(), "eino-agent-mailbox")
	}
	if teamName != "" {
		dir = filepath.Join(dir, teamName)
	}

	path := filepath.Join(dir, strings.ToLower(recipientName)+".jsonl")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	_ = os.Remove(path)

	var messages []MailboxMessage
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg MailboxMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// CreateShutdownRequestMessage creates a structured shutdown request.
func CreateShutdownRequestMessage(requestID, from, reason string) string {
	msg := map[string]string{
		"type":       "shutdown_request",
		"request_id": requestID,
		"from":       from,
		"reason":     reason,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(msg)
	return string(data)
}

// CreateShutdownApprovedMessage creates a structured shutdown approval.
func CreateShutdownApprovedMessage(requestID, from string) string {
	msg := map[string]string{
		"type":       "shutdown_approved",
		"request_id": requestID,
		"from":       from,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(msg)
	return string(data)
}

// CreateShutdownRejectedMessage creates a structured shutdown rejection.
func CreateShutdownRejectedMessage(requestID, from, reason string) string {
	msg := map[string]string{
		"type":       "shutdown_rejected",
		"request_id": requestID,
		"from":       from,
		"reason":     reason,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(msg)
	return string(data)
}
