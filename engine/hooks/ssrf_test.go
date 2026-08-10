package hooks

import (
	"testing"
)

func TestIsBlockedAddressIPv4(t *testing.T) {
	tests := []struct {
		addr    string
		blocked bool
		desc    string
	}{
		// Blocked ranges
		{"0.0.0.0", true, "this network"},
		{"0.1.2.3", true, "this network"},
		{"10.0.0.1", true, "private 10/8"},
		{"10.255.255.255", true, "private 10/8"},
		{"169.254.169.254", true, "link-local / cloud metadata"},
		{"169.254.0.1", true, "link-local"},
		{"172.16.0.1", true, "private 172.16/12"},
		{"172.31.255.255", true, "private 172.16/12 upper"},
		{"192.168.0.1", true, "private 192.168/16"},
		{"192.168.255.255", true, "private 192.168/16"},
		{"100.64.0.1", true, "CGNAT"},
		{"100.100.100.200", true, "CGNAT / Alibaba metadata"},
		{"100.127.255.255", true, "CGNAT upper"},

		// Allowed (loopback)
		{"127.0.0.1", false, "loopback"},
		{"127.255.255.255", false, "loopback upper"},

		// Allowed (public)
		{"8.8.8.8", false, "Google DNS"},
		{"1.1.1.1", false, "Cloudflare DNS"},
		{"203.0.113.1", false, "documentation range"},
		{"172.15.0.1", false, "just below private 172.16"},
		{"172.32.0.1", false, "just above private 172.31"},
		{"100.63.255.255", false, "just below CGNAT"},
		{"100.128.0.0", false, "just above CGNAT"},
		{"192.167.0.1", false, "not 192.168"},
	}

	for _, tt := range tests {
		got := IsBlockedAddress(tt.addr)
		if got != tt.blocked {
			t.Errorf("IsBlockedAddress(%q) [%s] = %v, want %v", tt.addr, tt.desc, got, tt.blocked)
		}
	}
}

func TestIsBlockedAddressIPv6(t *testing.T) {
	tests := []struct {
		addr    string
		blocked bool
		desc    string
	}{
		// Allowed
		{"::1", false, "loopback"},

		// Blocked
		{"::", true, "unspecified"},
		{"fc00::1", true, "unique local fc00"},
		{"fd12:3456::1", true, "unique local fd"},
		{"fe80::1", true, "link-local"},
		{"fe80::abcd:1234", true, "link-local with interface"},
		{"febf::1", true, "link-local upper"},

		// IPv4-mapped (blocked via v4 check)
		{"::ffff:169.254.169.254", true, "mapped cloud metadata"},
		{"::ffff:10.0.0.1", true, "mapped private"},
		{"::ffff:192.168.1.1", true, "mapped private 192.168"},

		// IPv4-mapped (allowed)
		{"::ffff:127.0.0.1", false, "mapped loopback"},
		{"::ffff:8.8.8.8", false, "mapped public"},

		// Public IPv6
		{"2001:db8::1", false, "documentation"},
		{"2607:f8b0:4004::1", false, "Google"},
	}

	for _, tt := range tests {
		got := IsBlockedAddress(tt.addr)
		if got != tt.blocked {
			t.Errorf("IsBlockedAddress(%q) [%s] = %v, want %v", tt.addr, tt.desc, got, tt.blocked)
		}
	}
}

func TestIsBlockedAddressInvalid(t *testing.T) {
	if IsBlockedAddress("not-an-ip") {
		t.Error("invalid address should not be blocked")
	}
	if IsBlockedAddress("") {
		t.Error("empty address should not be blocked")
	}
}

func TestSSRFErrorMessage(t *testing.T) {
	err := &SSRFError{Hostname: "metadata.internal", Address: "169.254.169.254"}
	msg := err.Error()
	if msg == "" {
		t.Error("error message should not be empty")
	}
	if !contains(msg, "blocked") {
		t.Error("error should mention 'blocked'")
	}
	if !contains(msg, "169.254.169.254") {
		t.Error("error should contain the address")
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"http://example.com/path", "example.com"},
		{"https://example.com:8080/path", "example.com"},
		{"http://user:pass@example.com/path", "example.com"},
		{"http://10.0.0.1:3000/hook", "10.0.0.1"},
		{"http://[::1]:8080/hook", "::1"},
		{"example.com", "example.com"},
	}

	for _, tt := range tests {
		got := extractHost(tt.url)
		if got != tt.want {
			t.Errorf("extractHost(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestValidateHookURLBlocked(t *testing.T) {
	err := ValidateHookURL("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Error("cloud metadata URL should be blocked")
	}
	if _, ok := err.(*SSRFError); !ok {
		t.Errorf("error should be *SSRFError, got %T", err)
	}
}

func TestValidateHookURLAllowedLoopback(t *testing.T) {
	err := ValidateHookURL("http://127.0.0.1:8080/hook")
	if err != nil {
		t.Errorf("loopback should be allowed, got %v", err)
	}
}

func TestValidateHookURLAllowedPublic(t *testing.T) {
	err := ValidateHookURL("http://8.8.8.8/hook")
	if err != nil {
		t.Errorf("public IP should be allowed, got %v", err)
	}
}

func TestSSRFGuardedDialerCreation(t *testing.T) {
	dialer := SSRFGuardedDialer()
	if dialer == nil {
		t.Error("SSRFGuardedDialer should return non-nil")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsCheck(s, substr))
}

func containsCheck(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
