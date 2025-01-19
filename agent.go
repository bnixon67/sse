package sse

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CommandHandlerFunc defines a func type that processes a command's payload.
type CommandHandlerFunc func(any)

// CmdHandlerFuncMap maps command names to respective handler functions.
type CmdHandlerFuncMap map[string]CommandHandlerFunc

// DefaultHeartbeatInterval is the standard time interval for an agent
// to send heartbeat signals to the server, indicating it is still active.
const DefaultHeartbeatInterval = 1 * time.Minute

// DefaultRetryInterval defines the default duration an agent waits
// before attempting to reconnect after a connection failure.
const DefaultRetryInterval = 30 * time.Second

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

	// Function hooks for customization
	ConnectAndReceiveFn func(ctx context.Context) error
	SendHeartbeatFn     func() error
}

var DefaultHTTPClient = &http.Client{
	Transport: &http.Transport{
		IdleConnTimeout: 90 * time.Second,
	},
	// Don't set Client Timeout connection should stay open.
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

// WithCustomConnectAndReceive allows a custom [ConnectAndReceive] function
func WithCustomConnectAndReceive(fn func(ctx context.Context) error) AgentOption {
	return func(a *Agent) {
		if fn == nil {
			return
		}
		a.ConnectAndReceiveFn = fn
	}
}

// WithCustomSendHeartbeat allows a custom [SendHeartbeat] function
func WithCustomSendHeartbeat(fn func() error) AgentOption {
	return func(a *Agent) {
		if fn == nil {
			return
		}
		a.SendHeartbeatFn = fn
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
			slog.Error("Connection error",
				"agentID", a.ID, "error", err)
		}

		select {
		case <-ctx.Done():
			slog.Info("Shutting down event loop", "agentID", a.ID)
			return
		case <-time.After(retryInterval):
			slog.Info("Retrying connection",
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

	query := u.Query()
	query.Set("agentID", agentID)
	u.RawQuery = query.Encode()

	return u.String(), nil
}

// sendRequest sends an HTTP request to the given endpoint using the agent's
// configuration. It sets the Authorization header and returns the HTTP
// response.
//
// The caller is responsible for closing the response body.
//
// An error is returned if the request fails or the server returns a non-200
// status.
func (a *Agent) sendRequest(ctx context.Context, method, endpoint string) (*http.Response, error) {
	u, err := buildURL(a.ServerURL, endpoint, a.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to create URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, nil)
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

	slog.Info("Connected to server", "agentID", a.ID)

	go a.startHeartbeat(ctx)

	// Process server-sent events
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if err := a.processServerEvent(line); err != nil {
			slog.Error("Failed to process server event",
				"error", err, "line", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading from server: %w", err)
	}

	slog.Info("Disconnected from server", "agentID", a.ID)

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

// startHeartbeat periodically sends heartbeat signals to the server at the
// configured interval until the provided context is canceled.
func (a *Agent) startHeartbeat(ctx context.Context) {
	slog.Info("Heartbeat started", "agentID", a.ID)

	heartbeatInterval := a.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = DefaultHeartbeatInterval
	}

	if a.SendHeartbeatFn == nil {
		slog.Error("SendHeartbeatFn is nil, skipping heartbeat",
			"agentID", a.ID)
		return
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Heartbeat stopped", "agentID", a.ID)
			return
		case <-ticker.C:
			slog.Debug("Sending heartbeat", "agentID", a.ID)
			if err := a.SendHeartbeatFn(); err != nil {
				slog.Error("Failed to send heartbeat",
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
// Note: Multi-line `data` fields are not currently supported.
func (a *Agent) processServerEvent(eventLine string) error {
	const dataPrefix = "data: "
	if !strings.HasPrefix(eventLine, dataPrefix) {
		return nil // Ignore lines that don't start with "data: "
	}

	// Extract the eventData after "data: "
	eventData := eventLine[len(dataPrefix):]

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
		slog.Warn("Unknown command", "name", msg.Command)
	}

	return nil
}
