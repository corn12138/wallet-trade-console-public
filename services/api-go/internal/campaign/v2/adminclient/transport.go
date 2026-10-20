package adminclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

var walletPattern = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// Client talks to one API base such as https://host/api. It never follows
// redirects, so a bearer token cannot be replayed to a host the operator did
// not name, and it refuses cleartext HTTP except on loopback test services.
type Client struct {
	base  *url.URL
	http  *http.Client
	token string
}

const maxResponseBytes = 1024 * 1024

func NewClient(base string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, errors.New("API base must be an absolute URL without credentials, query or fragment")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		host := parsed.Hostname()
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("cleartext HTTP is allowed only for a loopback API")
		}
	default:
		return nil, errors.New("API base must use https")
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{base: parsed, http: client}, nil
}

func (c *Client) Base() string { return c.base.String() }

// WithCredential binds a session to the API that issued it. Reusing a token
// against another base would send one deployment's credential to another.
func (c *Client) WithCredential(value Credential) (*Client, error) {
	if value.API != c.Base() {
		return nil, fmt.Errorf("stored session belongs to %s, not %s", value.API, c.Base())
	}
	copied := *c
	copied.token = value.Token
	return &copied, nil
}

// Reply is a raw HTTP observation. Err means the outcome of a write is unknown:
// the request may have reached the server even when no response arrived.
type Reply struct {
	Status    int
	RequestID string
	Body      []byte
	Err       error
}

func (c *Client) Do(ctx context.Context, method, path string, body []byte, requestID string) Reply {
	request, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, bytes.NewReader(body))
	if err != nil {
		return Reply{Err: err}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	if requestID != "" {
		request.Header.Set("X-Request-Id", requestID)
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Reply{Err: redactTransport(err)}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	reply := Reply{Status: response.StatusCode, RequestID: response.Header.Get("X-Request-Id"), Body: raw}
	if err != nil {
		reply.Err = redactTransport(err)
	} else if len(raw) > maxResponseBytes {
		reply.Err = errors.New("response exceeds 1 MiB")
	}
	return reply
}

// redactTransport keeps the failure class but drops URLs from net/http errors.
// The URL carries no secret today; stripping it keeps operator logs uniform.
func redactTransport(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("timeout")
	}
	return fmt.Errorf("transport failure: %v", err)
}

// ErrorCode returns the closed campaign error code only when the body satisfies
// the ErrorResponse contract; proxy pages and malformed errors yield ok=false.
func (r Reply) ErrorCode() (string, bool) {
	if r.Err != nil {
		return "", false
	}
	var value v2.ErrorResponse
	if err := v2.DecodeContractResponse("ErrorResponse", r.Body, &value); err != nil || value.Code == "" {
		return "", false
	}
	return value.Code, true
}

// Describe summarizes a non-success observation without echoing response
// bodies, which may come from an intermediary rather than the campaign API.
func (r Reply) Describe() string {
	if r.Err != nil {
		return r.Err.Error()
	}
	if code, ok := r.ErrorCode(); ok {
		return fmt.Sprintf("HTTP %d %s", r.Status, code)
	}
	return fmt.Sprintf("HTTP %d with a non-contract body", r.Status)
}

// GetContract performs an authenticated read and strictly decodes a named
// response contract. Any other observation is returned as an error.
func (c *Client) GetContract(ctx context.Context, path, contract string, target any) error {
	reply := c.Do(ctx, http.MethodGet, path, nil, readRequestID())
	if reply.Err == nil && reply.Status == http.StatusOK {
		if err := v2.DecodeContractResponse(contract, reply.Body, target); err != nil {
			return fmt.Errorf("%s response violates contract", contract)
		}
		return nil
	}
	return &ReadError{Reply: reply}
}

type ReadError struct{ Reply Reply }

// readRequestID gives every read a client-chosen correlation ID. Without one,
// the server's default generator embeds its host name, which would then be
// copied into operator records and evidence.
func readRequestID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return "campaign-admin-read-" + hex.EncodeToString(raw[:])
}

func (e *ReadError) Error() string { return "read failed: " + e.Reply.Describe() }
