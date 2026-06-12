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
// For plain HTTP, if the URL host is a name that resolves to multiple
// addresses (e.g. "localhost" → {127.0.0.1, ::1}), each address is
// tried in order until one accepts the TCP connection. This is the
// same address-fallback pattern we landed in the JDBC driver after the
// macOS-IPv6-by-default bug bit us.
//
// HTTPS endpoints keep the original hostname as the single candidate:
// replacing the URL host with a resolved IP address breaks TLS SNI and
// certificate hostname verification.
type HTTPTransport struct {
	uri            QuackURI
	httpClient     *http.Client
	requestTimeout time.Duration
}

// NewHTTPTransport constructs a transport using the URI's timeouts
// (or the package defaults when the URI carries none).
func NewHTTPTransport(uri QuackURI) *HTTPTransport {
	connectTimeout := uri.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = DefaultConnectTimeout
	}
	requestTimeout := uri.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = DefaultRequestTimeout
	}
	return &HTTPTransport{
		uri: uri,
		httpClient: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   connectTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		},
		requestTimeout: requestTimeout,
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

	endpoints, err := t.endpointCandidates()
	if err != nil {
		return nil, err
	}

	var lastFailure error
	for _, endpoint := range endpoints {
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
		return decoded, nil
	}

	detail := "no endpoints connected"
	if lastFailure != nil {
		detail = lastFailure.Error()
	}
	return nil, fmt.Errorf("transport: HTTP connect failed for %s:%d (tried %s): %s",
		t.uri.Host, t.uri.Port, strings.Join(endpoints, ", "), detail)
}

// endpointCandidates returns the endpoint URLs to attempt, in order.
// HTTPS keeps the hostname (one candidate, preserving SNI/cert
// verification); plain HTTP expands to every resolved address.
func (t *HTTPTransport) endpointCandidates() ([]string, error) {
	if t.uri.TLS {
		return []string{t.uri.HTTPURL()}, nil
	}
	addrs, err := net.LookupIP(t.uri.Host)
	if err != nil {
		return nil, fmt.Errorf("transport: resolve host %q: %w", t.uri.Host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("transport: no addresses found for %q", t.uri.Host)
	}
	endpoints := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		endpoints = append(endpoints, t.endpointFor(addr.String()))
	}
	return endpoints, nil
}

func (t *HTTPTransport) endpointFor(addr string) string {
	host := addr
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d%s", host, t.uri.Port, codec.QuackEndpoint)
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
