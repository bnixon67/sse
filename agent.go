package sse

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CommandHandlerMap maps command names to their handlers
type CommandHandlerMap map[string]func(any)

const (
	// DefaultHeartbeatInterval defines the default interval for heartbeats.
	DefaultHeartbeatInterval = 1 * time.Minute

	// DefaultRetryInterval defines the default interval for reconnection.
	DefaultRetryInterval = 30 * time.Second
)

// Agent represents a client that connects to a server to process events.
type Agent struct {
	ID                string            // Unique identifier for the agent.
	Token             string            // Bearer token for authentication.
	ServerURL         string            // Server's base URL.
	Handlers          CommandHandlerMap // Maps command names to handlers.
	HeartbeatInterval time.Duration     // Heartbeat interval.
	RetryInterval     time.Duration     // Retry interval for reconnection.
}

// ConnectAndReceiveWithReconnection connects to the server, processes events,
// and handles reconnection logic.
func (a *Agent) ConnectAndReceiveWithReconnection(ctx context.Context) {
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

// ConnectAndReceive connects to the server and processes events
func (a *Agent) ConnectAndReceive(ctx context.Context) error {
	url := fmt.Sprintf("%s/events?agentID=%s", a.ServerURL, a.ID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to connect: server returned status %d", resp.StatusCode)
	}

	slog.Info("Connected to server", "url", url, "agentID", a.ID)

	go a.startHeartbeat(ctx)

	// Process server-sent events
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if err := a.handleServerMessage(line); err != nil {
			slog.Error("Failed to handle server message",
				"error", err, "line", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading server response: %w", err)
	}

	slog.Info("Disconnected from server", "agentID", a.ID)
	return nil
}

// sendHeartbeat sends a heartbeat to the server.
func (a *Agent) sendHeartbeat() error {
	url := fmt.Sprintf("%s/heartbeat?agentID=%s", a.ServerURL, a.ID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)

	client := &http.Client{}
	resp, err := client.Do(req)
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

// startHeartbeat periodically sends heartbeats to the server.
// Stops sending heartbeats when the context is canceled.
func (a *Agent) startHeartbeat(ctx context.Context) {
	slog.Info("Heartbeat started", "agentID", a.ID)

	interval := a.HeartbeatInterval
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Heartbeat stopped", "agentID", a.ID)
			return
		case <-ticker.C:
			slog.Debug("Sending heartbeat", "agentID", a.ID)
			if err := a.sendHeartbeat(); err != nil {
				slog.Error("Failed to send heartbeat",
					"agentID", a.ID, "error", err)
			}
		}
	}
}

// validateMessage validates the structure of an API message.
func validateMessage(msg Message) error {
	if msg.Command == "" {
		return fmt.Errorf("missing command name")
	}

	if _, ok := msg.Params.(map[string]any); !ok {
		return fmt.Errorf("invalid data format")
	}

	return nil
}

// handleServerMessage parses and routes incoming messages.
// Multi-line data: is not supported.
func (a *Agent) handleServerMessage(line string) error {
	const prefix = "data: "
	if !strings.HasPrefix(line, prefix) {
		return nil // Ignore lines that don't start with "data: "
	}

	payload := line[len(prefix):] // Extract the payload after "data: "

	var msg Message
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return fmt.Errorf("failed to parse server message: %w", err)
	}

	if err := validateMessage(msg); err != nil {
		return fmt.Errorf("invalid message: %w", err)
	}

	if fn, exists := a.Handlers[msg.Command]; exists {
		fn(msg.Params)
	} else {
		slog.Warn("Unknown command", "name", msg.Command)
	}

	return nil
}
