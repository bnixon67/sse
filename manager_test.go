package sse

import (
	"net/http/httptest"
	"sync"
	"testing"
)

func TestAgentManager_RegisterUnregister(t *testing.T) {
	manager := NewManager(DefaultCleanupThreshold, "test-token")

	tests := []struct {
		name          string
		agentID       string
		action        func(agentID string)
		expectedCount int
	}{
		{
			name:    "Register a single agent",
			agentID: "agent-1",
			action: func(agentID string) {
				manager.Register(agentID, httptest.NewRecorder())
			},
			expectedCount: 1,
		},
		{
			name:    "Unregister a single agent",
			agentID: "agent-1",
			action: func(agentID string) {
				manager.Unregister(agentID)
			},
			expectedCount: 0,
		},
		{
			name:    "Re-register an agent after unregistration",
			agentID: "agent-1",
			action: func(agentID string) {
				manager.Register(agentID, httptest.NewRecorder())
				manager.Unregister(agentID)
				manager.Register(agentID, httptest.NewRecorder())
			},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.action(tt.agentID)
			if count := manager.Count(); count != tt.expectedCount {
				t.Errorf("expected %d agents, got %d", tt.expectedCount, count)
			}
		})
	}
}

func TestAgentManager_Send(t *testing.T) {
	manager := NewManager(DefaultCleanupThreshold, "test-token")

	tests := []struct {
		name          string
		agentID       string
		command       string
		params        any
		setup         func()
		expectSuccess bool
	}{
		{
			name:    "Send to a registered agent",
			agentID: "agent-1",
			command: "TestCommand",
			params:  map[string]string{"key": "value"},
			setup: func() {
				manager.Register("agent-1", httptest.NewRecorder())
			},
			expectSuccess: true,
		},
		{
			name:          "Send to an unregistered agent",
			agentID:       "nonexistent-agent",
			command:       "TestCommand",
			params:        map[string]string{"key": "value"},
			setup:         func() {},
			expectSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			err := manager.Send(tt.agentID, tt.command, tt.params)
			if (err == nil) != tt.expectSuccess {
				t.Errorf("unexpected result: got success=%v, want success=%v", err == nil, tt.expectSuccess)
			}
		})
	}
}

func TestAgentManager_Cleanup(t *testing.T) {
	manager := NewManager(DefaultCleanupThreshold, "test-token")
	var wg sync.WaitGroup

	// Register active and inactive agents
	activeAgent := httptest.NewRecorder()
	inactiveAgent := httptest.NewRecorder()

	manager.Register("active-agent", activeAgent)
	manager.Register("inactive-agent", inactiveAgent)

	wg.Add(1)
	go func() {
		defer wg.Done()
		close(manager.sessions["inactive-agent"].done)
	}()

	wg.Wait()

	manager.Cleanup()

	if count := manager.Count(); count != 1 {
		t.Errorf("expected 1 active agent after cleanup, got %d", count)
	}
}
