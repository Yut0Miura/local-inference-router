package backend

import (
	"net"
	"net/http"
	"time"
)

// Fixed transport timeouts. There is deliberately no overall client timeout:
// it would cut off long streamed model responses. The request context carries
// client cancellation instead.
const (
	DialTimeout         = 5 * time.Second
	TLSHandshakeTimeout = 5 * time.Second
	IdleConnTimeout     = 90 * time.Second

	HealthTimeout = 2 * time.Second
)

// NewClient builds an HTTP client for upstream inference requests.
// Redirects are returned to the caller instead of being followed, so a 3xx is
// handled as a normal upstream response.
func NewClient(responseHeaderTimeout time.Duration) *http.Client {
	return &http.Client{
		Transport: newTransport(responseHeaderTimeout),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewHealthClient builds the client used for health checks. It is independent
// of inference traffic and has a short overall timeout.
func NewHealthClient() *http.Client {
	return &http.Client{
		Transport: newTransport(HealthTimeout),
		Timeout:   HealthTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newTransport(responseHeaderTimeout time.Duration) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: DialTimeout}).DialContext
	transport.TLSHandshakeTimeout = TLSHandshakeTimeout
	transport.IdleConnTimeout = IdleConnTimeout
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.ForceAttemptHTTP2 = true
	return transport
}
