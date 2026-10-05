package agenticdeepseek

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
)

func TestTransportErrorHasSafeActionableCause(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"cancellation", context.Canceled, "canceled"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"network timeout", &net.DNSError{Name: "private-host", IsTimeout: true}, "timeout"},
		{"DNS", &net.DNSError{Name: "private-host", IsNotFound: true}, "dns"},
		{"TLS", x509.UnknownAuthorityError{}, "tls"},
		{"reset", fmt.Errorf("private-endpoint: %w", syscall.ECONNRESET), "connection_reset"},
		{"refused", syscall.ECONNREFUSED, "connection_refused"},
		{"EOF", io.EOF, "connection_closed"},
		{"unexpected EOF", io.ErrUnexpectedEOF, "connection_closed"},
		{"unknown", errors.New("private-token private-endpoint private-host"), "network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &transportError{err: tc.err}
			if !errors.Is(err, tc.err) {
				t.Fatal("transport wrapper lost the underlying error identity")
			}
			formatted := err.Error()
			if !strings.Contains(formatted, "reason="+tc.code) {
				t.Fatalf("error lacks safe cause %q: %s", tc.code, formatted)
			}
			for _, private := range []string{"private-token", "private-endpoint", "private-host"} {
				if strings.Contains(formatted, private) {
					t.Fatalf("transport error leaked %q", private)
				}
			}
		})
	}
}
