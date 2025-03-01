package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bnixon67/sloghandler"
	"github.com/bnixon67/sse"
)

// Function registry for handling commands.
var functionRegistry = sse.CmdHandlerFuncMap{
	"NewMonitor":    newMonitor,
	"UpdateMonitor": updateMonitor,
}

// newMonitor is the handler for the "NewMonitor" command.
func newMonitor(data interface{}) {
	slog.Info("Executing NewMonitor", "data", data)
}

// updateMonitor is the handler for the "UpdateMonitor" command.
func updateMonitor(data interface{}) {
	slog.Info("Executing UpdateMonitor", "data", data)
}

// initializeLogging sets up the default logger with a custom handler.
func initializeLogging() {
	handler := sloghandler.NewLogFormatHandler(slog.LevelDebug, os.Stdout, sloghandler.DefaultTimeFormat)
	logger := slog.New(handler)
	slog.SetDefault(logger)
}

// handleOSSignals listens for OS signals and cancels the provided context
// on signal receipt.
func handleOSSignals(cancelFunc context.CancelFunc) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-signalChan
		slog.Info("Received shutdown signal", "signal", sig)
		cancelFunc()
	}()
}

func main() {
	// Initialize logging.
	initializeLogging()

	// Create a cancellable context for managing application lifecycle.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize the SSE agent.
	agent := sse.NewAgent(
		"agent-1", "secret-token", "http://localhost:8080",
		functionRegistry,
		sse.WithHeartbeatInterval(10*time.Second),
		sse.WithRetryInterval(5*time.Second),
	)

	// Handle OS signals for graceful shutdown
	handleOSSignals(cancel)

	// Start the agent's main loop in a goroutine
	go func() {
		agent.ConnectAndReceiveRetry(ctx)
	}()

	// Wait for context cancellation (triggered by signal handling)
	<-ctx.Done()
	slog.Info("Application shutdown complete")
}
