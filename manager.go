package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Message defines the structure for server-to-agent messages.
type Message struct {
	RequestID string `json:"request_id"`
	Command   string `json:"function_name"`
	Params    any    `json:"params"`
}

// agent holds the ResponseWriter, a channel for signaling disconnection,
// and the timestamp of the last received heartbeat.
type agent struct {
	writer        http.ResponseWriter
	done          chan struct{}
	lastHeartbeat time.Time
}

// DefaultCleanupThreshold is the default max duration before an inactive
// agent is removed.
const DefaultCleanupThreshold = 5 * time.Minute

// Manager manages a collection of agents and their connections.
type Manager struct {
	mu               sync.RWMutex
	agents           map[string]*agent
	cleanupThreshold time.Duration // Max duration for inactive agent
	token            string        // Token for authentication
}

// NewManager initializes and returns a new Manager.
func NewManager(d time.Duration, token string) *Manager {
	if d <= 0 {
		d = DefaultCleanupThreshold
	}

	return &Manager{
		agents:           make(map[string]*agent),
		cleanupThreshold: d,
		token:            token,
	}
}

// Register registers a new agent or updates an existing connection.
func (m *Manager) Register(agentID string, w http.ResponseWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close existing connection if the agent reconnects.
	if oldAgent, exists := m.agents[agentID]; exists {
		close(oldAgent.done)
		delete(m.agents, agentID)
		slog.Info("Agent disconnected", "agentID", agentID)
	}

	m.agents[agentID] = &agent{
		writer:        w,
		done:          make(chan struct{}),
		lastHeartbeat: time.Now(),
	}

	slog.Info("Agent connected", "agentID", agentID)
}

// Unregister removes an agent from the manager.
func (m *Manager) Unregister(agentID string) {
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
func (m *Manager) Send(agentID string, command string, params any) error {
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
		if err := m.attemptSend(agentID, command, params); err == nil {
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
func (m *Manager) attemptSend(agentID string, command string, params any) error {
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
		Params:    params,
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
func (m *Manager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for agentID, agent := range m.agents {
		select {
		case <-agent.done:
			// Remove disconnected clients.
			delete(m.agents, agentID)
			slog.Info("Cleaned up inactive agent",
				"agentID", agentID)
		default:
			// Remove inactive agents
			if now.Sub(agent.lastHeartbeat) > m.cleanupThreshold {
				close(agent.done)
				delete(m.agents, agentID)
				slog.Info("Agent removed due to inactivity",
					"agentID", agentID)
			}
		}
	}
}

// Count returns the number of connected agents.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.agents)
}

// Example token validation function
func (m *Manager) validateToken(token string) bool {
	return token == m.token
}

// EventsHandler creates an HTTP handler for managing a Server-Sent Events
// (SSE) connection. It registers the client with the given Manager
// and handles cleanup when the connection is closed.
func (m *Manager) EventsHandler(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if token == "" || !strings.HasPrefix(token, "Bearer ") {
		slog.Error("missing token")
		http.Error(w, "Unauthorized: Missing token",
			http.StatusUnauthorized)
		return
	}

	// Extract and validate the token
	providedToken := strings.TrimPrefix(token, "Bearer ")
	if !m.validateToken(providedToken) {
		slog.Error("invalid token")
		http.Error(w, "Unauthorized: Invalid token",
			http.StatusUnauthorized)
		return
	}

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
func (m *Manager) GetAgentDoneChannel(agentID string) <-chan struct{} {
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
// Manager to remove inactive agents. It runs at the specified interval
// until the context is canceled.
func (m *Manager) CleanupRoutine(interval time.Duration, ctx context.Context) {
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
func (m *Manager) HeartbeatHandler(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentID")
	if agentID == "" {
		http.Error(w, "Agent ID missing", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if agent, exists := m.agents[agentID]; exists {
		agent.lastHeartbeat = time.Now()
		slog.Info("Heartbeat received", "agentID", agentID)
		w.WriteHeader(http.StatusOK)
	} else {
		http.Error(w, "Agent not connected", http.StatusNotFound)
	}

}

// Get retrieves an agent by ID.
func (m *Manager) Get(agentID string) (*agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	agent, exists := m.agents[agentID]
	return agent, exists
}
