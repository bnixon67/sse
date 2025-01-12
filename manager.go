package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Message defines the structure for server-to-agent messages.
type Message struct {
	RequestID string      `json:"request_id"`
	Command   string      `json:"function_name"`
	Data      interface{} `json:"data"`
}

// agent holds the ResponseWriter and a channel for signaling disconnection.
type agent struct {
	writer http.ResponseWriter
	done   chan struct{}
}

// AgentManager manages a collection of agents and their connections.
type AgentManager struct {
	mu     sync.RWMutex
	agents map[string]*agent
}

// NewAgentManager initializes and returns a new AgentManager.
func NewAgentManager() *AgentManager {
	return &AgentManager{
		agents: make(map[string]*agent),
	}
}

// Register registers a new agent or updates an existing connection.
func (m *AgentManager) Register(agentID string, w http.ResponseWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close existing connection if the agent reconnects.
	if oldClient, exists := m.agents[agentID]; exists {
		close(oldClient.done)
		delete(m.agents, agentID)
		slog.Info("Agent disconnected", "agentID", agentID)
	}

	m.agents[agentID] = &agent{writer: w, done: make(chan struct{})}

	slog.Info("Agent connected", "agentID", agentID)
}

// Unregister removes an agent from the manager.
func (m *AgentManager) Unregister(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if agent, exists := m.agents[agentID]; exists {
		close(agent.done)
		delete(m.agents, agentID)
		slog.Info("Agent disconnected", "agentID", agentID)
	} else {
		slog.Warn("Agent does not exist", "agentID", agentID)
	}
}

// Send attempts to send a command with data to the agent identified by agentID.
func (m *AgentManager) Send(agentID string, command string, data interface{}) error {
	const (
		maxRetries    = 3           // Maximum number of retry attempts
		baseBackoff   = time.Second // Base backoff duration
		backoffFactor = 2           // Exponential backoff multiplier
	)

	var lastErr error

	// Ensure agent is unregistered if all attempts fail
	defer func() {
		if lastErr != nil {
			m.Unregister(agentID)
		}
	}()

	// Retry logic with exponential backoff
	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Attempt to send the command
		if err := m.attemptSend(agentID, command, data); err == nil {
			return nil // Success
		} else {
			lastErr = fmt.Errorf("attempt %d failed: %w", attempt, err)
		}

		// Apply backoff for subsequent retries
		if attempt < maxRetries {
			backoff := baseBackoff * time.Duration(backoffFactor*attempt)
			time.Sleep(backoff)
		}
	}

	// All attempts failed
	return fmt.Errorf("failed to send command %q to agent %q after %d attempts: %w", command, agentID, maxRetries, lastErr)
}

// attemptSend attempts to send a command with data to the specified agent.
func (m *AgentManager) attemptSend(agentID string, command string, data interface{}) error {
	// Acquire a read lock to safely access the agent map
	m.mu.RLock()
	agent, exists := m.agents[agentID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not connected", agentID)
	}

	// Prepare the command message
	msg := Message{
		RequestID: fmt.Sprintf("%d", time.Now().UnixNano()),
		Command:   command,
		Data:      data,
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	// Safely write to the client's ResponseWriter.
	select {
	case <-agent.done:
		// Agent connection is closed
		return fmt.Errorf("agent %s connection closed", agentID)
	default:
		_, err = fmt.Fprintf(agent.writer, "data: %s\n\n", jsonData)
		if err != nil {
			m.Unregister(agentID) // Cleanup if writing fails.
			return fmt.Errorf("failed to send command to agent %s: %w", agentID, err)
		}

		// Ensure data is flushed to the client immediately
		if flusher, ok := agent.writer.(http.Flusher); ok {
			flusher.Flush()
		} else {
			return fmt.Errorf("ResponseWriter does not support flushing for agent %s", agentID)
		}
	}

	slog.Info("Command sent to agent",
		"agentID", agentID, "command", command)
	return nil
}

// Cleanup removes inactive agents from the manager.
func (m *AgentManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for agentID, agent := range m.agents {
		select {
		case <-agent.done:
			// Remove disconnected clients.
			delete(m.agents, agentID)
			slog.Info("Cleaned up inactive agent",
				"agentID", agentID)
		default:
			// Client is active.
		}
	}
}

// Count returns the number of connected agents.
func (m *AgentManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.agents)
}

// EventsHandler creates an HTTP handler for managing a Server-Sent Events
// (SSE) connection. It registers the client with the given AgentManager
// and handles cleanup when the connection is closed.
func (m *AgentManager) EventsHandler(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentID")
	if agentID == "" {
		http.Error(w, "Agent ID missing", http.StatusBadRequest)
		return
	}

	// Set headers for SSE.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Register the client and ensure cleanup on exit
	m.Register(agentID, w)
	defer m.Unregister(agentID)

	// Check if the ResponseWriter supports CloseNotifier
	closeNotifier, ok := w.(http.CloseNotifier)
	if !ok {
		http.Error(w, "Server does not support close notifications", http.StatusInternalServerError)
		return
	}

	// Monitor for disconnection or cancellation
	select {
	case <-r.Context().Done():
		slog.Info("Request context canceled",
			"agentID", agentID)
	case <-closeNotifier.CloseNotify():
		slog.Info("Connection closed by client",
			"agentID", agentID)
	case <-m.GetAgentDoneChannel(agentID):
		slog.Info("Connection closed by server",
			"agentID", agentID)
	}
}

// GetAgentDoneChannel returns the `done` channel of the specified agent,
// which signals when the agent's connection is closed. If the agent does
// not exist, it returns a closed channel to prevent blocking.
func (m *AgentManager) GetAgentDoneChannel(agentID string) <-chan struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if agent, exists := m.agents[agentID]; exists {
		return agent.done
	}

	// Return a closed channel if agent doesn't exist to prevent blocking
	return closedChannel()
}

// closedChannel creates and returns a closed channel of type `chan struct{}`.
func closedChannel() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// CleanupRoutine periodically calls the Cleanup method on the provided
// AgentManager to remove inactive agents. It runs at the specified interval
// until the context is canceled.
func (m *AgentManager) CleanupRoutine(interval time.Duration, ctx context.Context) {
	if interval <= 0 {
		slog.Warn("Invalid cleanup interval, skipping routine")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("Starting cleanup routine", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Cleanup routine shutting down")
			return
		case <-ticker.C:
			slog.Info("Running cleanup")
			m.Cleanup()
		}
	}
}

// HeartbeatHandler handles periodic heartbeat signals from agents.
func (m *AgentManager) HeartbeatHandler(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentID")
	if agentID == "" {
		http.Error(w, "Agent ID missing", http.StatusBadRequest)
		return
	}

	// Log the received heartbeat.
	slog.Info("Heartbeat received", "agentID", agentID)

	// Confirm the agent is still connected.
	if _, exists := m.Get(agentID); !exists {
		http.Error(w, "Agent not connected", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// Get retrieves an agent's data by ID.
func (m *AgentManager) Get(agentID string) (*agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	agent, exists := m.agents[agentID]
	return agent, exists
}
