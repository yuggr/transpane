package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuggr/transpane/internal/config"
)

const maxResponseBytes = 4 * 1024 * 1024

// Option customizes a Client.
type Option func(*clientOptions)

type clientOptions struct {
	transport http.RoundTripper
	connect   time.Duration
	read      time.Duration
	total     time.Duration
}

// WithTransport replaces the HTTP transport (used by tests).
func WithTransport(transport http.RoundTripper) Option {
	return func(options *clientOptions) { options.transport = transport }
}

// WithTimeouts overrides the connect, read-idle and total limits.
func WithTimeouts(connect, read, total time.Duration) Option {
	return func(options *clientOptions) {
		options.connect = connect
		options.read = read
		options.total = total
	}
}

// Client is a single-endpoint PLaMo client.
type Client struct {
	baseURL   string
	model     string
	form      string
	http      *http.Client
	options   clientOptions
	transport *http.Transport
}

// NewClient builds a client with no proxy, no redirects, no auth and no
// retries beyond the transport's own safe behavior.
func NewClient(baseURL, model, form string, options ...Option) *Client {
	settings := clientOptions{
		connect: time.Duration(config.ConnectTimeoutSeconds * float64(time.Second)),
		read:    time.Duration(config.ReadTimeoutSeconds * float64(time.Second)),
		total:   time.Duration(config.TotalTimeoutSeconds * float64(time.Second)),
	}
	for _, option := range options {
		option(&settings)
	}

	var shared *http.Transport
	roundTripper := settings.transport
	if roundTripper == nil {
		dialer := &net.Dialer{Timeout: settings.connect}
		shared = &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				connection, err := dialer.DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return &readTimeoutConn{Conn: connection, timeout: settings.read}, nil
			},
			ResponseHeaderTimeout: settings.read,
			TLSHandshakeTimeout:   settings.connect,
		}
		roundTripper = shared
	}

	client := &http.Client{
		Transport: roundTripper,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		model:     model,
		form:      form,
		http:      client,
		options:   settings,
		transport: shared,
	}
}

// readTimeoutConn applies an idle read deadline to each body read.
type readTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *readTimeoutConn) Read(buffer []byte) (int, error) {
	if c.timeout > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Read(buffer)
}

func (c *Client) systemPrompt() string {
	return "[plamo-translate]\nsource_lang=" + config.SourceLang + "\ntarget_lang=" + config.TargetLang + "\nform=" + c.form
}

// ListModels returns every string model ID in data.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.total)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, failure(ErrorKindConnect)
	}
	response, err := c.do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, failure(ErrorKindHTTP, response.StatusCode)
	}
	body, err := readBody(response.Body, ctx)
	if err != nil {
		return nil, err
	}
	payload, ok := decodeJSON(body)
	if !ok {
		return nil, failure(ErrorKindInvalid)
	}
	data, ok := payload["data"].([]any)
	if !ok {
		return nil, failure(ErrorKindInvalid)
	}
	models := []string{}
	for _, entry := range data {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := object["id"].(string); ok {
			models = append(models, id)
		}
	}
	return models, nil
}

// Translate sends exactly one system prompt and one user segment.
func (c *Client) Translate(ctx context.Context, text string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.total)
	defer cancel()
	payload := struct {
		Model    string    `json:"model"`
		Messages []message `json:"messages"`
		Stream   bool      `json:"stream"`
	}{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: c.systemPrompt()},
			{Role: "user", Content: text},
		},
		Stream: false,
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return "", failure(ErrorKindInvalid)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", &encoded)
	if err != nil {
		return "", failure(ErrorKindConnect)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", failure(ErrorKindHTTP, response.StatusCode)
	}
	body, err := readBody(response.Body, ctx)
	if err != nil {
		return "", err
	}
	return parseCompletion(body)
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *Client) do(request *http.Request) (*http.Response, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, classifyTransport(request.Context(), err)
	}
	return response, nil
}

func readBody(body io.Reader, ctx context.Context) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, classifyTransport(ctx, err)
	}
	if len(raw) > maxResponseBytes {
		return nil, failure(ErrorKindInvalid)
	}
	if !utf8.Valid(raw) {
		return nil, failure(ErrorKindInvalid)
	}
	return raw, nil
}

func decodeJSON(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

func parseCompletion(raw []byte) (string, error) {
	payload, ok := decodeJSON(raw)
	if !ok {
		return "", failure(ErrorKindInvalid)
	}
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return "", failure(ErrorKindInvalid)
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return "", failure(ErrorKindInvalid)
	}
	if finish, _ := choice["finish_reason"].(string); finish != "stop" {
		return "", failure(ErrorKindIncomplete)
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return "", failure(ErrorKindInvalid)
	}
	content, ok := message["content"].(string)
	if !ok {
		return "", failure(ErrorKindInvalid)
	}
	if strings.TrimSpace(content) == "" {
		return "", failure(ErrorKindEmpty)
	}
	return strings.TrimSpace(content), nil
}

func classifyTransport(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failure(ErrorKindTimeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return failure(ErrorKindCancelled)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return failure(ErrorKindTimeout)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failure(ErrorKindTimeout)
	}
	if errors.Is(err, context.Canceled) {
		return failure(ErrorKindCancelled)
	}
	return failure(ErrorKindConnect)
}

// Close releases idle connections.
func (c *Client) Close() error {
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return nil
}
