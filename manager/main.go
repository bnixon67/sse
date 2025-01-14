package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bnixon67/sloghandler"
	"github.com/bnixon67/sse"
)

func main() {
	initializeLogging()

	// Create a context with cancel functionality
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize the AgentManager
	m := sse.NewManager(time.Minute, "secret-token")

	// Set up signal handling for graceful shutdown
	handleOSSignals(cancel)

	// Create the HTTP server with a context-aware shutdown
	server := &http.Server{
		Addr:    ":8080",
		Handler: createHandlers(m),
	}

	// Start the cleanup routine in a separate goroutine
	go m.CleanupRoutine(30*time.Second, ctx)

	// Start the simulated command-sending routine
	go startCommandSender(ctx, m, "agent-1")

	// Run the server in a goroutine
	go func() {
		slog.Info("Starting server", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Server encountered an error", "error", err)
			cancel() // Trigger shutdown if the server crashes
		}
	}()

	// Wait for context cancellation (triggered by signal handling)
	<-ctx.Done()
	slog.Info("Shutting down application")

	// Gracefully shut down the server
	shutdownServer(server)
}

// initializeLogging sets up the default logger with structured logging
func initializeLogging() {
	handler := sloghandler.NewLogFormatHandler(slog.LevelDebug, os.Stdout)
	logger := slog.New(handler)
	slog.SetDefault(logger)
}

// handleOSSignals listens for OS signals and cancels the provided context on signal receipt
func handleOSSignals(cancelFunc context.CancelFunc) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-signalChan
		slog.Info("Received shutdown signal", "signal", sig)
		cancelFunc()
	}()
}

// createHandlers registers HTTP handlers for the server
func createHandlers(m *sse.Manager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/events", m.EventsHandler)
	mux.HandleFunc("/heartbeat", m.HeartbeatHandler)
	return mux
}

// startCommandSender simulates sending periodic commands to an agent
func startCommandSender(ctx context.Context, manager *sse.Manager, agentID string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Command sender shutting down")
			return
		case <-ticker.C:
			sendCommand(ctx, manager, agentID, "NewMonitor", map[string]interface{}{
				"monitor_id": "123",
				"url":        "http://example.com",
			})
			time.Sleep(7 * time.Second) // Simulate delay before sending the next command
			sendCommand(ctx, manager, agentID, "UpdateMonitor", map[string]interface{}{
				"monitor_id": "456",
				"url":        "http://example.com",
			})
		}
	}
}

// sendCommand sends a command to the specified agent and logs the result
func sendCommand(ctx context.Context, manager *sse.Manager, agentID, command string, data map[string]interface{}) {
	if err := manager.Send(agentID, command, data); err != nil {
		slog.Error("Failed to send command", "agentID", agentID, "command", command, "error", err)
	} else {
		slog.Info("Command sent successfully", "agentID", agentID, "command", command)
	}
}

// shutdownServer gracefully shuts down the HTTP server
func shutdownServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("Error shutting down server", "error", err)
	} else {
		slog.Info("Server shutdown complete")
	}
}
