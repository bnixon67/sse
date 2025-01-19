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

// Agent represents a client that connects to a server using Server-Sent
// Events (SSE). It provides methods to manage the connection and send
// heartbeat signals.
type Agent interface {
	// ConnectAndReceiveRetry establishes a persistent connection to
	// the server, continuously listening for messages. If the connection
	// is lost, it automatically retries until the provided context
	// is canceled.
	ConnectAndReceiveRetry(ctx context.Context)

	// ConnectAndReceive connects to the server and listens for incoming
	// events. It processes messages until the connection is lost or the
	// provided context is canceled. Returns an error if the connection
	// cannot be established.
	ConnectAndReceive(ctx context.Context) error

	// SendHeartbeat transmits a heartbeat signal to the server to indicate
	// an active connection. Returns an error if the request fails.
	SendHeartbeat() error
}

// AgentOption represents a functional option for configuring an Agent instance.
type AgentOption func(*agent)

// agent is a client that connects to a server to receive and process events.
type agent struct {
	ID                string            // Unique identifier for the agent.
	Token             string            // Bearer token for authentication.
	ServerURL         string            // Base URL of the SSE server.
	Handlers          CmdHandlerFuncMap // Maps command names to handlers.
	HeartbeatInterval time.Duration     // Interval for hearbeat signals.
	RetryInterval     time.Duration     // Interval for reconnect attempts.
	Client            *http.Client      // HTTP client for requests.
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
func NewAgent(id, token, serverURL string, handlers CmdHandlerFuncMap, opts ...AgentOption) Agent {
	if handlers == nil {
		handlers = make(CmdHandlerFuncMap)
	}

	a := &agent{
		ID:                id,
		Token:             token,
		ServerURL:         serverURL,
		Handlers:          handlers,
		HeartbeatInterval: DefaultHeartbeatInterval,
		RetryInterval:     DefaultRetryInterval,
		Client:            &http.Client{},
		// Don't set Client Timeout connection should stay open.
	}

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
	return func(a *agent) {
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
	return func(a *agent) {
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
	return func(a *agent) {
		if client == nil {
			return
		}

		a.Client = client
	}
}

// ConnectAndReceiveRetry establishes a connection to the server and listens
// for events. If the connection is lost, it automatically attempts to
// reconnect. The function runs until the provided context is canceled.
func (a *agent) ConnectAndReceiveRetry(ctx context.Context) {
	retryInterval := a.RetryInterval
	if retryInterval <= 0 {
		retryInterval = DefaultRetryInterval
	}

	for {
		if err := a.ConnectAndReceive(ctx); err != nil {
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

// ConnectAndReceive establishes a connection to the server and processes
// incoming events. It listens for events until the connection is lost
// or the provided context is canceled. Returns an error if the connection
// cannot be established.
func (a *agent) ConnectAndReceive(ctx context.Context) error {
	u, err := buildURL(a.ServerURL, "/events", a.ID)
	if err != nil {
		return fmt.Errorf("failed to create URL: %w", err)
	}

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)

	resp, err := a.Client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send %s: %w", u, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send, status %d", resp.StatusCode)
	}

	slog.Info("Connected to server", "url", u, "agentID", a.ID)

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
func (a *agent) SendHeartbeat() error {
	u, err := buildURL(a.ServerURL, "/heartbeat", a.ID)
	if err != nil {
		return fmt.Errorf("failed to create URL: %w", err)
	}

	req, err := http.NewRequest("POST", u, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)

	resp, err := a.Client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat failed with status code: %d",
			resp.StatusCode)
	}

	return nil
}

// startHeartbeat periodically sends heartbeat signals to the server at the
// configured interval until the provided context is canceled.
func (a *agent) startHeartbeat(ctx context.Context) {
	slog.Info("Heartbeat started", "agentID", a.ID)

	ticker := time.NewTicker(a.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Heartbeat stopped", "agentID", a.ID)
			return
		case <-ticker.C:
			slog.Debug("Sending heartbeat", "agentID", a.ID)
			if err := a.SendHeartbeat(); err != nil {
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
		return fmt.Errorf("invalid parameters format")
	}

	return nil
}

// processServerEvent routes an incoming server event to the appropriate
// handler.
//
// Note: Multi-line `data` fields are not currently supported.
func (a *agent) processServerEvent(eventLine string) error {
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
