package sse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAgent_ConnectAndReceive(t *testing.T) {
	wantExecutions := 3

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		for i := 0; i < wantExecutions; i++ {
			_, _ = w.Write([]byte("data: {\"function_name\":\"TestCommand\",\"params\":{\"key\":\"value\"}}\n\n"))
			flusher.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer server.Close()

	gotExecutions := 0
	agent := &Agent{
		ID:        "test-agent",
		Token:     "token",
		ServerURL: server.URL,
		Handlers: CommandHandlerMap{
			"TestCommand": func(params any) {
				gotExecutions = gotExecutions + 1

				var tp struct{ Key string }
				raw, err := json.Marshal(params)
				if err != nil {
					t.Fatal("failed to marshal data:", err)
					return
				}
				if err := json.Unmarshal(raw, &tp); err != nil {
					t.Fatal("failed to unmarshaldata:", err)
					return
				}

				wantKey := "value"
				if tp.Key != wantKey {
					t.Fatalf("got Key %s, want %s",
						tp.Key, wantKey)

				}
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := agent.ConnectAndReceive(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotExecutions != wantExecutions {
		t.Errorf("gotExecutions %d, want %d", gotExecutions, wantExecutions)
	}
}
