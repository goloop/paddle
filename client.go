package paddle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// API hosts and the API version this package speaks.
const (
	ProductionURL = "https://api.paddle.com"
	SandboxURL    = "https://sandbox-api.paddle.com"

	// DefaultVersion is sent as the Paddle-Version header.
	DefaultVersion = "1"
)

// Client defaults.
const (
	DefaultTimeout    = 30 * time.Second
	DefaultMaxRetries = 2
)

// sandboxKeyPrefix starts every sandbox API key. A key says which
// environment it belongs to, so the client can pick the host itself and a
// sandbox key never ends up talking to production by a missing option.
const sandboxKeyPrefix = "pdl_sdbx_"

// maxResponse bounds how much of a response body is read.
const maxResponse = 4 << 20

// maxRetryAfter caps how long a Retry-After header can make a call wait.
const maxRetryAfter = 60 * time.Second

// Client calls the Paddle Billing API.
//
// A Client is safe for concurrent use.
type Client struct {
	apiKey     string
	baseURL    string
	version    string
	http       *http.Client
	customHTTP bool
	maxRetries int
	headers    http.Header
	sleep      func(context.Context, time.Duration) error
}

// Option configures a Client in New.
type Option func(*Client)

// WithSandbox points the client at the sandbox (true) or production (false)
// host, overriding what the API key implies.
func WithSandbox(sandbox bool) Option {
	return func(c *Client) {
		if sandbox {
			c.baseURL = SandboxURL
		} else {
			c.baseURL = ProductionURL
		}
	}
}

// WithBaseURL overrides the API host (a proxy, a test server).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http, c.customHTTP = h, true }
}

// WithTimeout sets the per-request timeout of the default HTTP client. It
// has no effect together with WithHTTPClient.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if !c.customHTTP {
			c.http.Timeout = d
		}
	}
}

// WithMaxRetries sets how many times a request is retried. See the package
// documentation for which failures are retried.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = max(0, n) }
}

// WithVersion overrides the Paddle-Version header.
func WithVersion(v string) Option {
	return func(c *Client) { c.version = v }
}

// WithHeader adds a header sent with every request.
func WithHeader(key, value string) Option {
	return func(c *Client) { c.headers.Add(key, value) }
}

// New returns a Client for the API key. The host is the sandbox for a
// sandbox key ("pdl_sdbx_...") and production otherwise; WithSandbox and
// WithBaseURL override it.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:     apiKey,
		baseURL:    ProductionURL,
		version:    DefaultVersion,
		http:       &http.Client{Timeout: DefaultTimeout},
		maxRetries: DefaultMaxRetries,
		headers:    http.Header{},
		sleep:      sleepCtx,
	}
	if strings.HasPrefix(apiKey, sandboxKeyPrefix) {
		c.baseURL = SandboxURL
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Sandbox reports whether the client talks to the sandbox host.
func (c *Client) Sandbox() bool { return c.baseURL == SandboxURL }

// envelope is the shape of every successful response.
type envelope struct {
	Data json.RawMessage `json:"data"`
}

// do sends one request and decodes data into out.
//
// Only failures that cannot have changed anything are retried: a 429 (the
// request was refused before it ran) on any method, and a network error or
// a 5xx on a GET. A POST that failed with a 5xx may have been applied, and
// the API takes no idempotency key, so repeating it could create a second
// transaction or a second refund - the caller decides, with the error in
// hand.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("paddle: encode request: %w", err)
		}
	}

	for attempt := 0; ; attempt++ {
		status, data, header, err := c.send(ctx, method, path, body)
		retry := false
		var wait time.Duration
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			retry = method == http.MethodGet
			err = fmt.Errorf("paddle: %s %s: %w", method, path, err)
		case status == http.StatusTooManyRequests:
			retry = true
			wait = retryAfter(header.Get("Retry-After"))
			err = parseError(status, data)
		case status >= 500:
			retry = method == http.MethodGet
			err = parseError(status, data)
		case status >= 300:
			return parseError(status, data)
		default:
			if out == nil {
				return nil
			}
			var env envelope
			if err := json.Unmarshal(data, &env); err != nil {
				return fmt.Errorf("paddle: decode response: %w", err)
			}
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("paddle: decode response: %w", err)
			}
			return nil
		}

		if !retry || attempt >= c.maxRetries {
			return err
		}
		if wait == 0 {
			wait = backoff(attempt)
		}
		if serr := c.sleep(ctx, wait); serr != nil {
			return serr
		}
	}
}

func (c *Client) send(ctx context.Context, method, path string, body []byte) (int, []byte, http.Header, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, vs := range c.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if c.version != "" {
		req.Header.Set("Paddle-Version", c.version)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return 0, nil, nil, err
	}
	return res.StatusCode, data, res.Header, nil
}

// backoff is 0.5s, 1s, 2s, ... with jitter, capped at 8s.
func backoff(attempt int) time.Duration {
	d := min(500*time.Millisecond<<attempt, 8*time.Second)
	return d/2 + rand.N(d/2+1)
}

// retryAfter reads a Retry-After header in seconds; anything else falls back
// to the default backoff.
func retryAfter(v string) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || s <= 0 {
		return 0
	}
	return min(time.Duration(s)*time.Second, maxRetryAfter)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func checkID(what, id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty %s id", ErrInvalid, what)
	}
	if strings.ContainsAny(id, "/?#") {
		return fmt.Errorf("%w: malformed %s id %q", ErrInvalid, what, id)
	}
	return nil
}

// ErrInvalid reports a request refused before it was sent.
var ErrInvalid = errors.New("paddle: invalid request")
