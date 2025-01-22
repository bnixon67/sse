package sse

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultRetryInterval defines how long an agent waits before attempting
// to reconnect after a connection failure.
const DefaultRetryInterval = 30 * time.Second

// DefaultIdleConnTimeout defines how long an idle connection remains open
// before being closed. It should be greater than DefaultHeartbeatInterval
// to prevent the connection from being closed due to inactivity.
const DefaultIdleConnTimeout = 5 * time.Minute

// DefaultHeartbeatInterval defines the interval at which heartbeat messages
// should be sent to keep the connection alive. It should be less than
// DefaultIdleConnTimeout to prevent the connection from being closed due
// to inactivity.
const DefaultHeartbeatInterval = 1 * time.Minute

// DefaultExpectContinueTimeout controls how long the client waits for an
// HTTP/1.1 "100 Continue" response. Since SSE connections are long-lived,
// this should be short to avoid unnecessary delays.
const DefaultExpectContinueTimeout = 1 * time.Second

// DefaultResponseHeaderTimeout defines how long to wait for a server's
// response headers. Should generally be unset (0) for SSE to avoid
// premature timeouts.
const DefaultResponseHeaderTimeout = 0

// DefaultDialerTimeout defines the maximum amount of time a dial will wait
// for a connection.
const DefaultDialerTimeout = 10 * time.Second

// DefaultHTTPClient is the default HTTP client for an Agent.
//
// It sets an idle connection timeout but omits a client timeout,
// ensuring connections remain open indefinitely. This is essential
// for Server-Sent Events (SSE) and other long-lived connections.
var DefaultHTTPClient = &http.Client{
	Transport: &http.Transport{
		IdleConnTimeout:       DefaultIdleConnTimeout,
		ExpectContinueTimeout: DefaultExpectContinueTimeout,
		ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
		DialContext: (&net.Dialer{
			Timeout: DefaultDialerTimeout,
		}).DialContext,
	},
}

// CommandHandlerFunc represents a function that processes a command.
type CommandHandlerFunc func(any)

// CmdHandlerFuncMap maps command names to respective handler functions.
type CmdHandlerFuncMap map[string]CommandHandlerFunc

// AgentOption represents a functional option for configuring an Agent instance.
type AgentOption func(*Agent)

// Agent is a client that connects to a server using Server-Sent Events
// (SSE) to receive and process events.
type Agent struct {
	ID                string            // Unique identifier for the agent.
	Token             string            // Bearer token for authentication.
	ServerURL         string            // Base URL of the SSE server.
	Handlers          CmdHandlerFuncMap // Maps command names to handlers.
	HeartbeatInterval time.Duration     // Interval for hearbeat signals.
	RetryInterval     time.Duration     // Interval for reconnect attempts.
	Client            *http.Client      // HTTP client for requests.
	Logger            *slog.Logger      // Custom logger

	// Function hooks for customization
	ConnectAndReceiveFn func(ctx context.Context) error
	SendHeartbeatFn     func() error
}

// NewAgent creates a new Agent instance with the specified ID, token, and
// server URL. It uses the provided command handler map to dispatch commands
// to their respective functions. Additional configuration options can be
// applied using functional options (AgentOption).
//
// If nil is provided for handlers, an empty CmdHandlerFuncMap is used.
//
// If no functional options are provided, the agent is initialized with default
// settings, including [DefaultHeartbeatInterval], [DefaultRetryInterval],
// and a default HTTP client.
func NewAgent(id, token, serverURL string, handlers CmdHandlerFuncMap, opts ...AgentOption) *Agent {
	if handlers == nil {
		handlers = make(CmdHandlerFuncMap)
	}

	a := &Agent{
		ID:                id,
		Token:             token,
		ServerURL:         serverURL,
		Handlers:          handlers,
		HeartbeatInterval: DefaultHeartbeatInterval,
		RetryInterval:     DefaultRetryInterval,
		Client:            DefaultHTTPClient,
		Logger:            slog.Default(),
	}

	// Set default function implementations
	a.ConnectAndReceiveFn = a.ConnectAndReceive
	a.SendHeartbeatFn = a.SendHeartbeat

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// WithHeartbeatInterval sets the heartbeat interval for an Agent.
//
// If the provided interval is zero or negative, the existing heartbeat
// interval remains unchanged.
func WithHeartbeatInterval(interval time.Duration) AgentOption {
	return func(a *Agent) {
		if interval <= 0 {
			return
		}
		a.HeartbeatInterval = interval
	}
}

// WithRetryInterval sets the retry interval for an Agent.
//
// If the provided interval is zero or negative, the existing retry interval
// remains unchanged.
func WithRetryInterval(interval time.Duration) AgentOption {
	return func(a *Agent) {
		if interval <= 0 {
			return
		}
		a.RetryInterval = interval
	}
}

// WithClient sets the HTTP client for an Agent.
//
// If nil is provided, the existing client remains unchanged.
func WithClient(client *http.Client) AgentOption {
	return func(a *Agent) {
		if client == nil {
			return
		}
		a.Client = client
	}
}

// WithCustomConnectAndReceive sets a custom ConnectAndReceive function.
//
// If nil is provided, the existing function remains unchanged.
func WithCustomConnectAndReceive(fn func(ctx context.Context) error) AgentOption {
	return func(a *Agent) {
		if fn == nil {
			return
		}
		a.ConnectAndReceiveFn = fn
	}
}

// WithCustomSendHeartbeat sets a custom SendHeartbeat function
//
// If nil is provided, the existing function remains unchanged.
func WithCustomSendHeartbeat(fn func() error) AgentOption {
	return func(a *Agent) {
		if fn == nil {
			return
		}
		a.SendHeartbeatFn = fn
	}
}

// WithLogger allows setting a custom logger for the Agent.
//
// If nil is provided, the default logger will be used.
func WithLogger(logger *slog.Logger) AgentOption {
	return func(a *Agent) {
		if logger == nil {
			a.Logger = slog.Default()
		}
		a.Logger = logger
	}
}

// ConnectAndReceiveRetry establishes a connection to the server and listens
// for events. If the connection is lost, it automatically attempts to
// reconnect. The function runs until the provided context is canceled.
func (a *Agent) ConnectAndReceiveRetry(ctx context.Context) {
	retryInterval := a.RetryInterval
	if retryInterval <= 0 {
		retryInterval = DefaultRetryInterval
	}

	for {
		if err := a.ConnectAndReceiveFn(ctx); err != nil {
			a.Logger.Error("Connection error",
				"agentID", a.ID, "error", err)
		}

		select {
		case <-ctx.Done():
			a.Logger.Info("Shutting down event loop",
				"agentID", a.ID)
			return
		case <-time.After(retryInterval):
			a.Logger.Info("Retrying connection",
				"agentID", a.ID, "retryInterval", retryInterval)
		}
	}
}

// buildURL constructs the URL with the agent ID as a query parameter.
func buildURL(baseURL, path, agentID string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	u.Path, err = url.JoinPath(u.Path, path)
	if err != nil {
		return "", fmt.Errorf("failed to join path: %w", err)
	}

	q := u.Query()
	q.Set("agentID", agentID)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// sendRequest sends an HTTP request to the specified endpoint using the
// agent's configuration.
//
// It constructs the request URL based on the agent’s server URL and ID,
// sets the Authorization header with the agent's bearer token, and executes
// the request using the configured HTTP client.
//
// The caller is responsible for closing the response body.
//
// If the request fails or the server responds with a non-200 status, an
// error is returned, and the response body is closed internally. On success,
// the caller is responsible for closing the response body.
func (a *Agent) sendRequest(ctx context.Context, method, endpoint string) (*http.Response, error) {
	u, err := buildURL(a.ServerURL, endpoint, a.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to create URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)

	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", u, err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("request to %s failed with status code: %d", u, resp.StatusCode)
	}

	return resp, nil
}

// ConnectAndReceive establishes a connection to the server and processes
// incoming events. It listens for events until the connection is lost
// or the provided context is canceled. Returns an error if the connection
// cannot be established.
func (a *Agent) ConnectAndReceive(ctx context.Context) error {
	resp, err := a.sendRequest(ctx, "GET", "/events")
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer resp.Body.Close()

	a.Logger.Info("Connected to server", "agentID", a.ID)

	go a.StartHeartbeat(ctx)

	// Process server-sent events
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if err := a.processServerEvent(line); err != nil {
			a.Logger.Error("Failed to process server event",
				"error", err, "line", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading from server: %w", err)
	}

	a.Logger.Info("Disconnected from server", "agentID", a.ID)

	return nil
}

// SendHeartbeat notifies the server that the agent is active. Returns an
// error if the request fails.
func (a *Agent) SendHeartbeat() error {
	resp, err := a.sendRequest(context.Background(), "POST", "/heartbeat")
	if err != nil {
		return fmt.Errorf("heartbeat failed: %w", err)
	}
	defer resp.Body.Close()

	return nil
}

// StartHeartbeat periodically sends a heartbeat request to the server at the
// configured HeartbeatInterval. It runs until the provided context is canceled.
func (a *Agent) StartHeartbeat(ctx context.Context) {
	if a.SendHeartbeatFn == nil {
		a.Logger.Warn("SKipping heartbeat, SendHeartbeatFn is nil",
			"agentID", a.ID)
		return
	}

	a.Logger.Info("Heartbeat started", "agentID", a.ID)

	heartbeatInterval := a.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = DefaultHeartbeatInterval
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.Logger.Info("Heartbeat stopped", "agentID", a.ID)
			return
		case <-ticker.C:
			a.Logger.Debug("Sending heartbeat", "agentID", a.ID)
			if err := a.SendHeartbeatFn(); err != nil {
				a.Logger.Error("Failed to send heartbeat",
					"agentID", a.ID, "error", err)
			}
		}
	}
}

// validateMessage checks whether a Message is valid. It returns an error
// if the message is missing a command or has an invalid parameter format.
func validateMessage(m Message) error {
	if m.Command == "" {
		return fmt.Errorf("missing command name")
	}

	if _, ok := m.Params.(map[string]any); !ok {
		return fmt.Errorf("invalid parameters format: expected map[string]any, got %T", m.Params)
	}

	return nil
}

// processServerEvent routes an incoming server event to the appropriate
// handler.
//
// Note: Multi-line `data` fields are not supported.
func (a *Agent) processServerEvent(eventLine string) error {
	const prefix = "data: "
	if !strings.HasPrefix(eventLine, prefix) {
		return nil // Ignore lines that don't start with "data: "
	}

	// Extract the eventData
	eventData := eventLine[len(prefix):]

	var msg Message
	if err := json.Unmarshal([]byte(eventData), &msg); err != nil {
		return fmt.Errorf("failed to parse server message: %w", err)
	}

	if err := validateMessage(msg); err != nil {
		return fmt.Errorf("invalid message: %w", err)
	}

	// Dispatch command to function.
	if fn, exists := a.Handlers[msg.Command]; exists {
		fn(msg.Params)
	} else {
		a.Logger.Warn("Unknown command", "name", msg.Command)
	}

	return nil
}
