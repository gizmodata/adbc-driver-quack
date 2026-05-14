package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/message"
)

// ErrServerError signals that the Quack server returned an explicit
// ERROR_RESPONSE.
type ErrServerError struct {
	Message string
}

func (e *ErrServerError) Error() string { return e.Message }

// HTTPTransport posts framed binary Quack messages to /quack over
// HTTP(S) and returns the decoded server response.
//
// If the URL host is a name that resolves to multiple addresses (e.g.
// "localhost" → {127.0.0.1, ::1}), each address is tried in order until
// one accepts the TCP connection. This is the same address-fallback
// pattern we landed in the JDBC driver after the macOS-IPv6-by-default
// bug bit us.
type HTTPTransport struct {
	uri            QuackURI
	httpClient     *http.Client
	requestTimeout time.Duration
}

// NewHTTPTransport constructs a transport with sensible defaults.
func NewHTTPTransport(uri QuackURI) *HTTPTransport {
	return &HTTPTransport{
		uri: uri,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		},
		requestTimeout: 60 * time.Second,
	}
}

// SetHTTPClient lets tests inject a custom client (e.g. with shorter timeouts).
func (t *HTTPTransport) SetHTTPClient(c *http.Client) {
	t.httpClient = c
}

// Send posts a Quack message and returns the decoded server reply.
func (t *HTTPTransport) Send(ctx context.Context, m message.QuackMessage) (message.QuackMessage, error) {
	body, err := message.EncodeMessage(m)
	if err != nil {
		return nil, fmt.Errorf("transport: encode message: %w", err)
	}

	addrs, err := net.LookupIP(t.uri.Host)
	if err != nil {
		return nil, fmt.Errorf("transport: resolve host %q: %w", t.uri.Host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("transport: no addresses found for %q", t.uri.Host)
	}

	var lastFailure error
	for i, addr := range addrs {
		endpoint := t.endpointFor(addr.String())
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if reqErr != nil {
			return nil, fmt.Errorf("transport: build HTTP request: %w", reqErr)
		}
		req.Header.Set("Content-Type", codec.DuckDBMIMEType)
		req.Header.Set("Accept", codec.DuckDBMIMEType)

		resp, sendErr := t.httpClient.Do(req)
		if sendErr != nil {
			if isFallthroughErr(sendErr) {
				lastFailure = sendErr
				continue
			}
			return nil, fmt.Errorf("transport: HTTP request to %s: %w", endpoint, sendErr)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("transport: read response from %s: %w", endpoint, readErr)
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("transport: HTTP %d from %s", resp.StatusCode, endpoint)
		}
		decoded, decErr := message.DecodeMessage(respBody)
		if decErr != nil {
			return nil, fmt.Errorf("transport: decode response: %w", decErr)
		}
		if err, ok := decoded.(message.ErrorResponse); ok {
			return nil, &ErrServerError{Message: err.Message}
		}
		_ = i
		return decoded, nil
	}

	addrsList := make([]string, 0, len(addrs))
	for _, a := range addrs {
		addrsList = append(addrsList, a.String())
	}
	detail := "no addresses connected"
	if lastFailure != nil {
		detail = lastFailure.Error()
	}
	return nil, fmt.Errorf("transport: HTTP connect failed for %s:%d (tried %s): %s",
		t.uri.Host, t.uri.Port, strings.Join(addrsList, ", "), detail)
}

func (t *HTTPTransport) endpointFor(addr string) string {
	scheme := "http"
	if t.uri.TLS {
		scheme = "https"
	}
	host := addr
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s://%s:%d%s", scheme, host, t.uri.Port, codec.QuackEndpoint)
}

// isFallthroughErr reports whether a Send failure is connection-level
// (worth trying the next resolved address) rather than something the
// server actually returned.
func isFallthroughErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	// Wrap text-match check as a fallback for net/http's wrapped errors.
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connect: ") ||
		strings.Contains(s, "no route to host") ||
		strings.Contains(s, "i/o timeout")
}
