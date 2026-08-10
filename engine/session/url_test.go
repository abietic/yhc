package session

import "testing"

func TestParseSessionIdentifierUUID(t *testing.T) {
	result := ParseSessionIdentifier("550e8400-e29b-41d4-a716-446655440000")
	if result == nil {
		t.Fatal("expected non-nil")
		return
	}
	if result.SessionID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("got session ID %q", result.SessionID)
	}
	if result.IsURL || result.IsJSONLFile {
		t.Error("should not be URL or JSONL")
	}
}

func TestParseSessionIdentifierJSONL(t *testing.T) {
	result := ParseSessionIdentifier("/tmp/session.jsonl")
	if result == nil {
		t.Fatal("expected non-nil")
		return
	}
	if !result.IsJSONLFile {
		t.Error("should be JSONL file")
	}
	if result.JSONLFile != "/tmp/session.jsonl" {
		t.Errorf("got JSONL path %q", result.JSONLFile)
	}
	if result.SessionID == "" {
		t.Error("should have generated a session ID")
	}
}

func TestParseSessionIdentifierURL(t *testing.T) {
	result := ParseSessionIdentifier("https://api.example.com/v1/session/abc123")
	if result == nil {
		t.Fatal("expected non-nil")
		return
	}
	if !result.IsURL {
		t.Error("should be URL")
	}
	if result.IngressURL != "https://api.example.com/v1/session/abc123" {
		t.Errorf("got URL %q", result.IngressURL)
	}
}

func TestParseSessionIdentifierInvalid(t *testing.T) {
	if ParseSessionIdentifier("") != nil {
		t.Error("empty string should return nil")
	}
	if ParseSessionIdentifier("not-a-uuid-or-url") != nil {
		t.Error("random string should return nil")
	}
}
