package agenticdeepseek

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"syscall"
)

type transportPhase uint32

const (
	transportPhaseUnknown transportPhase = iota
	transportPhaseDNS
	transportPhaseConnect
	transportPhaseTLS
	transportPhaseUpload
	transportPhaseRequestSent
	transportPhaseHeaders
)

func (phase transportPhase) code() string {
	switch phase {
	case transportPhaseDNS:
		return "dns"
	case transportPhaseConnect:
		return "connect"
	case transportPhaseTLS:
		return "tls_handshake"
	case transportPhaseUpload:
		return "request_upload"
	case transportPhaseRequestSent:
		return "request_sent"
	case transportPhaseHeaders:
		return "response_headers"
	default:
		return "unknown"
	}
}

// transportReason exposes only a finite category. The original error stays
// available through Unwrap for cancellation/retry classification, but its
// endpoint, host, headers, and arbitrary text never enter the public message.
func transportReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var networkErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkErr) && networkErr.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var unknownAuthority x509.UnknownAuthorityError
	var invalidCertificate x509.CertificateInvalidError
	var hostname x509.HostnameError
	if errors.As(err, &unknownAuthority) || errors.As(err, &invalidCertificate) || errors.As(err, &hostname) {
		return "tls"
	}
	switch {
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed), errors.Is(err, syscall.EPIPE):
		return "connection_closed"
	default:
		return "network"
	}
}
