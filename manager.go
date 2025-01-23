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

// Message represents a command sent from the server to an agent.
type Message struct {
	ID      string // Unique identifier for the message
	Command string // Name of the command
	Params  any    // Parameters for the command
}

// Session maintains an agent's connection details.
type Session struct {
	writer        http.ResponseWriter
	done          chan struct{}
	lastHeartbeat time.Time
}

// DefaultCleanupThreshold specifies the maximum duration an inactive session
// is retained before being removed.
const DefaultCleanupThreshold = 5 * time.Minute

// Manager handles the lifecycle of agent sessions.
type Manager struct {
	mu               sync.RWMutex        // Ensures thread-safe access
	sessions         map[string]*Session // Active sessions indexed by ID
	cleanupThreshold time.Duration       // Inactive session removal
	token            string              // Token for agent validation
	logger           *slog.Logger        // Custom logger
}

// NewManager creates a new Manager instance with the specified cleanup
// threshold and authentication token.
func NewManager(d time.Duration, token string) *Manager {
	if d <= 0 {
		d = DefaultCleanupThreshold
	}

	return &Manager{
		sessions:         make(map[string]*Session),
		cleanupThreshold: d,
		token:            token,
		logger:           slog.Default(),
	}
}

// Register creates a new session for the specified agent ID or updates an
// existing one. If the agent is already connected, the previous session
// is closed and replaced.
func (m *Manager) Register(agentID string, w http.ResponseWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close existing connection if the session reconnects.
	if oldAgent, exists := m.sessions[agentID]; exists {
		close(oldAgent.done)
		delete(m.sessions, agentID)
		m.logger.Info("Agent disconnected", "agentID", agentID)
	}

	m.sessions[agentID] = &Session{
		writer:        w,
		done:          make(chan struct{}),
		lastHeartbeat: time.Now(),
	}

	m.logger.Info("Agent connected", "agentID", agentID)
}

// Unregister removes the session for the specified agent ID.
// If the agent is connected, its session is closed and deleted.
func (m *Manager) Unregister(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, exists := m.sessions[agentID]; exists {
		close(session.done)
		delete(m.sessions, agentID)
		m.logger.Info("Agent disconnected", "agentID", agentID)
	} else {
		m.logger.Warn("Agent does not exist", "agentID", agentID)
	}
}

// Send delivers a command with parameters to the specified agent. Returns an
// error if the agent is not connected or if the message cannot be sent.
func (m *Manager) Send(agentID string, command string, params any) error {
	const (
		maxRetries    = 3           // Maximum number of retry attempts
		baseBackoff   = time.Second // Base backoff duration
		backoffFactor = 2           // Exponential backoff multiplier
	)

	var lastErr error

	// Ensure session is unregistered if all attempts fail
	defer func() {
		if lastErr != nil {
			m.logger.Warn("Unregistering after failed retries",
				"agentID", agentID)
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

// attemptSend tries to deliver a command with parameters to the specified
// agent. Returns an error if the agent is not connected or if the message
// cannot be sent.
func (m *Manager) attemptSend(agentID string, command string, params any) error {
	// Acquire a read lock to safely access the session map
	m.mu.RLock()
	session, exists := m.sessions[agentID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not connected", agentID)
	}

	// Prepare the command message
	msg := Message{
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Command: command,
		Params:  params,
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	// Safely write to the client's ResponseWriter.
	select {
	case <-session.done:
		// Agent connection is closed
		return fmt.Errorf("agent %s connection closed", agentID)
	default:
		_, err = fmt.Fprintf(session.writer, "data: %s\n\n", jsonData)
		if err != nil {
			m.Unregister(agentID) // Cleanup if writing fails.
			return fmt.Errorf("failed to send command to agent %s: %w", agentID, err)
		}

		// Ensure data is flushed to the client immediately
		if flusher, ok := session.writer.(http.Flusher); ok {
			flusher.Flush()
		} else {
			m.logger.Warn("Flushing not supported", "agentID", agentID)
		}
	}

	m.logger.Info("Command sent",
		"agentID", agentID,
		"requestID", msg.ID,
		"command", command,
		"params", msg.Params)

	return nil
}

// Cleanup removes inactive agent sessions that have exceeded the cleanup
// threshold. Sessions are considered inactive if they have not received
// a heartbeat within the threshold period.
func (m *Manager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for agentID, session := range m.sessions {
		select {
		case <-session.done:
			// Remove disconnected clients.
			delete(m.sessions, agentID)
			m.logger.Info("Cleaned up inactive agent",
				"agentID", agentID)
		default:
			// Remove inactive sessions
			if now.Sub(session.lastHeartbeat) > m.cleanupThreshold {
				m.logger.Info("Agent marked for removal due to inactivity", "agentID", agentID)
				m.Unregister(agentID)
			}
		}
	}
}

// Count returns the total number of active agent sessions.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// validateToken checks whether the provided token matches the expected
// authentication token. Returns true if the token is valid, otherwise
// returns false.
func (m *Manager) validateToken(token string) bool {
	return token == m.token
}

// validateRequestToken extracts and validates the authentication token from
// the request. Returns an error if the token is missing or invalid.
func (m *Manager) validateRequestToken(r *http.Request) error {
	token := r.Header.Get("Authorization")
	if token == "" || !strings.HasPrefix(token, "Bearer ") {
		m.logger.Error("missing token")
		return fmt.Errorf("unauthorized: missing token")
	}

	providedToken := strings.TrimPrefix(token, "Bearer ")
	if !m.validateToken(providedToken) {
		m.logger.Error("invalid token")
		return fmt.Errorf("unauthorized: invalid token")
	}

	return nil
}

// EventsHandler manages a Server-Sent Events (SSE) connection for an agent.
// It authenticates the request, registers the agent, and ensures proper
// cleanup when the connection closes.
func (m *Manager) EventsHandler(w http.ResponseWriter, r *http.Request) {
	if err := m.validateRequestToken(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	agentID := r.URL.Query().Get("agentID")
	if agentID == "" {
		m.logger.Warn("Agent ID missing in request", "remoteAddr", r.RemoteAddr, "headers", r.Header)
		http.Error(w, "Agent ID missing", http.StatusBadRequest)
		return
	}

	// Set headers for SSE.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	m.Register(agentID, w)
	defer m.Unregister(agentID)

	closeNotifier, ok := w.(http.CloseNotifier)
	if !ok {
		http.Error(w, "Server does not support close notifications", http.StatusInternalServerError)
		return
	}

	select {
	case <-r.Context().Done():
		m.logger.Info("Request context canceled",
			"agentID", agentID)
	case <-closeNotifier.CloseNotify():
		m.logger.Info("Connection closed by client",
			"agentID", agentID)
	case <-m.GetAgentDoneChannel(agentID):
		m.logger.Info("Connection closed by server",
			"agentID", agentID)
	}
}

// GetAgentDoneChannel returns a read-only channel that signals when the
// specified agent's session is closed. If the agent does not exist, it
// returns a closed channel to prevent blocking.
func (m *Manager) GetAgentDoneChannel(agentID string) <-chan struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if session, exists := m.sessions[agentID]; exists {
		return session.done
	}

	// Return a closed channel if agent doesn't exist to prevent blocking
	return closedCh
}

// closedCh is a pre-closed channel used as a fallback when an agent session
// does not exist. This prevents goroutines from blocking on reads from a
// missing session's done channel.
var closedCh = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// CleanupRoutine periodically invokes the Cleanup method to remove inactive
// agent sessions. It runs at the specified interval until the provided
// context is canceled.
func (m *Manager) CleanupRoutine(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		m.logger.Warn("Invalid cleanup interval, skipping routine")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	m.logger.Info("Starting cleanup routine", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("Cleanup routine shutting down")
			return
		case <-ticker.C:
			m.logger.Info("Running cleanup")
			m.Cleanup()
		}
	}
}

// HeartbeatHandler processes heartbeat signals from agents to indicate
// active connections. It updates the last heartbeat timestamp for the
// specified agent or returns an error if the agent is not connected.
func (m *Manager) HeartbeatHandler(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentID")
	if agentID == "" {
		http.Error(w, "Agent ID missing", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if session, exists := m.sessions[agentID]; exists {
		session.lastHeartbeat = time.Now()
		m.logger.Info("Heartbeat received", "agentID", agentID)
		w.WriteHeader(http.StatusOK)
	} else {
		http.Error(w, "Agent not connected", http.StatusNotFound)
	}

}

// Get returns the session associated with the specified agent ID. The second
// return value indicates whether the session exists.
func (m *Manager) Get(agentID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, exists := m.sessions[agentID]
	return session, exists
}
