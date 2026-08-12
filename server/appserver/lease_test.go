package appserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionLeaseRejectsSecondOwnerAndUsesAdmittedTranscriptDir(t *testing.T) {
	transcriptDir := t.TempDir()
	first, err := acquireSessionLease(transcriptDir, "session", "server-a")
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if got, want := first.path, filepath.Join(transcriptDir, "session", ".app-server.lock"); got != want {
		t.Fatalf("lease path = %q, want %q", got, want)
	}
	if _, err := acquireSessionLease(transcriptDir, "session", "server-b"); err == nil {
		t.Fatal("second lease unexpectedly succeeded")
	}
	if err := first.close(); err != nil {
		t.Fatalf("release first lease: %v", err)
	}
	second, err := acquireSessionLease(transcriptDir, "session", "server-b")
	if err != nil {
		t.Fatalf("lease after release: %v", err)
	}
	if err := second.close(); err != nil {
		t.Fatalf("release second lease: %v", err)
	}
}

func TestSessionLeaseRejectsUnsafeIdentity(t *testing.T) {
	if _, err := acquireSessionLease(t.TempDir(), "../session", "server"); err == nil {
		t.Fatal("unsafe session identifier unexpectedly acquired a lease")
	}
}

func TestSessionLeaseRejectsDotPathSessionIdentifiers(t *testing.T) {
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{".", ".."} {
		t.Run(sessionID, func(t *testing.T) {
			lease, err := acquireSessionLease(transcriptDir, sessionID, "server")
			if err == nil {
				_ = lease.close()
				t.Fatalf("unsafe session identifier %q unexpectedly acquired a lease", sessionID)
			}
		})
	}
}
