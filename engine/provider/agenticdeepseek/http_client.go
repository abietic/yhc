package agenticdeepseek

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Keep slow TLS establishment inside the caller's request/cancellation budget
// without retrying a billable request or modifying other providers' transport.
const (
	defaultTLSHandshakeTimeout = 30 * time.Second
	defaultTCPDialBudget       = 30 * time.Second
)

var defaultTransportCache struct {
	sync.Mutex
	source    *http.Transport
	transport *http.Transport
}

func newDefaultHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport
	if standard, ok := transport.(*http.Transport); ok {
		// Reuse the pool across model roles and Files clients, as the standard
		// global transport did. Each client still owns its request timeout.
		defaultTransportCache.Lock()
		if defaultTransportCache.source != standard {
			cloned := standard.Clone()
			cloned.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
			if cloned.DialContext != nil {
				cloned.DialContext = retryTimedOutDial(cloned.DialContext)
			}
			defaultTransportCache.source = standard
			defaultTransportCache.transport = cloned
		}
		transport = defaultTransportCache.transport
		defaultTransportCache.Unlock()
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func retryTimedOutDial(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		// Reserve part of the original TCP budget for a fresh socket. The
		// HTTP transport cannot send the request until it receives a socket.
		dialCtx, cancel := context.WithTimeout(ctx, defaultTCPDialBudget)
		defer cancel()
		firstCtx, cancelFirst := context.WithTimeout(dialCtx, defaultTCPDialBudget/2)
		conn, err := dial(firstCtx, network, address)
		cancelFirst()

		var timeout net.Error
		if conn != nil || dialCtx.Err() != nil || !errors.As(err, &timeout) || !timeout.Timeout() {
			return conn, err
		}
		return dial(dialCtx, network, address)
	}
}
