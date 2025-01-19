package sse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestNewAgent(t *testing.T) {
	defaultHandlers := make(CmdHandlerFuncMap)
	testHandlers := CmdHandlerFuncMap{
		"TestCommand": func(params any) { return },
	}

	tests := []struct {
		name          string
		id            string
		token         string
		serverURL     string
		handlers      CmdHandlerFuncMap
		options       []AgentOption
		wantHandlers  CmdHandlerFuncMap
		wantHeartbeat time.Duration
		wantRetry     time.Duration
		wantClient    *http.Client
	}{
		{
			name:          "Default",
			id:            "default id",
			token:         "default token",
			serverURL:     "default url",
			handlers:      nil,
			options:       nil,
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{},
		},
		{
			name:          "Handlers",
			id:            "handlers id",
			token:         "handlers token",
			serverURL:     "handlers url",
			handlers:      testHandlers,
			options:       nil,
			wantHandlers:  testHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{},
		},
		{
			name:      "Custom heartbeat interval",
			id:        "heartbeat id",
			token:     "heartbeat token",
			serverURL: "heartbeat url",
			handlers:  nil,
			options: []AgentOption{
				WithHeartbeatInterval(42 * time.Second),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: 42 * time.Second,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{},
		},
		{
			name:      "Zero heartbeat interval",
			id:        "zero heartbeat id",
			token:     "zero heartbeat token",
			serverURL: "zero heartbeat url",
			handlers:  nil,
			options: []AgentOption{
				WithHeartbeatInterval(0 * time.Second),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{},
		},
		{
			name:      "Negative heartbeat interval",
			id:        "negative heartbeat id",
			token:     "negative heartbeat token",
			serverURL: "negative heartbeat url",
			handlers:  nil,
			options: []AgentOption{
				WithHeartbeatInterval(-1 * time.Second),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{},
		},
		{
			name:      "Custom retry interval",
			id:        "retry id",
			token:     "retry token",
			serverURL: "retry url",
			handlers:  nil,
			options: []AgentOption{
				WithRetryInterval(13 * time.Second),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     13 * time.Second,
			wantClient:    &http.Client{},
		},
		{
			name:      "Zero retry interval",
			id:        "zero id",
			token:     "zero token",
			serverURL: "zero url",
			handlers:  nil,
			options: []AgentOption{
				WithRetryInterval(0 * time.Second),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{},
		},
		{
			name:      "Negative retry interval",
			id:        "negative id",
			token:     "negative token",
			serverURL: "negative url",
			handlers:  nil,
			options: []AgentOption{
				WithRetryInterval(-1 * time.Second),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{},
		},
		{
			name:      "Custom HTTP client",
			id:        "client id",
			token:     "client token",
			serverURL: "client url",
			handlers:  nil,
			options: []AgentOption{
				WithClient(
					&http.Client{Timeout: 7 * time.Second},
				),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{Timeout: 7 * time.Second},
		},
		{
			name:      "Nil HTTP client",
			id:        "nil id",
			token:     "nil token",
			serverURL: "nil url",
			handlers:  nil,
			options: []AgentOption{
				WithClient(nil),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    &http.Client{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := NewAgent(tc.id, tc.token, tc.serverURL, tc.handlers, tc.options...).(*agent)

			if agent.ID != tc.id {
				t.Errorf("got ID %s, want %s", agent.ID, tc.id)
			}
			if agent.Token != tc.token {
				t.Errorf("got Token %s, want %s", agent.Token, tc.token)
			}
			if agent.ServerURL != tc.serverURL {
				t.Errorf("got ServerURL %s, want %s", agent.ServerURL, tc.serverURL)
			}
			if !reflect.DeepEqual(agent.Handlers, tc.wantHandlers) {
				t.Errorf("got Handlers %+v, want %+v", agent.Handlers, tc.wantHandlers)
			}
			if agent.HeartbeatInterval != tc.wantHeartbeat {
				t.Errorf("got HeartbeatInterval %v, want %v", agent.HeartbeatInterval, tc.wantHeartbeat)
			}
			if agent.RetryInterval != tc.wantRetry {
				t.Errorf("got RetryInterval %v, want %v", agent.RetryInterval, tc.wantRetry)
			}
			if !reflect.DeepEqual(agent.Client, tc.wantClient) {
				t.Errorf("got Client %+v, want %+v", agent.Client, tc.wantClient)
			}
		})
	}
}

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
			_, _ = w.Write([]byte("data: {\"command\":\"TestCommand\",\"params\":{\"key\":\"value\"}}\n\n"))
			flusher.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer server.Close()

	gotExecutions := 0
	agent := NewAgent("test-agent", "token", server.URL,
		CmdHandlerFuncMap{
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
	)

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
