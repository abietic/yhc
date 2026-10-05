package agenticdeepseek

import (
	"net/http"
	"sync"
	"time"
)

// Keep slow TLS establishment inside the caller's request/cancellation budget
// without retrying a billable request or modifying other providers' transport.
const defaultTLSHandshakeTimeout = 30 * time.Second

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
			defaultTransportCache.source = standard
			defaultTransportCache.transport = cloned
		}
		transport = defaultTransportCache.transport
		defaultTransportCache.Unlock()
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}
