package sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
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
			wantClient:    DefaultHTTPClient,
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
			wantClient:    DefaultHTTPClient,
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
			wantClient:    DefaultHTTPClient,
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
			wantClient:    DefaultHTTPClient,
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
			wantClient:    DefaultHTTPClient,
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
			wantClient:    DefaultHTTPClient,
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
			wantClient:    DefaultHTTPClient,
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
			wantClient:    DefaultHTTPClient,
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
			wantClient:    DefaultHTTPClient,
		},
		{
			name:      "Nil Custom ConnectAndReceive",
			id:        "Nil ConnectAndReceive id",
			token:     "Nil ConnectAndReceive token",
			serverURL: "Nil ConnectAndReceive url",
			handlers:  nil,
			options: []AgentOption{
				WithCustomConnectAndReceive(nil),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    DefaultHTTPClient,
		},
		{
			name:      "Custom ConnectAndReceive",
			id:        "ConnectAndReceive id",
			token:     "ConnectAndReceive token",
			serverURL: "ConnectAndReceive url",
			handlers:  nil,
			options: []AgentOption{
				WithCustomConnectAndReceive(
					func(ctx context.Context) error {
						return nil
					}),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    DefaultHTTPClient,
		},
		{
			name:      "Nil Custom SendHeartbeat",
			id:        "Nil SendHeartbeat id",
			token:     "Nil SendHeartbeat token",
			serverURL: "Nil SendHeartbeat url",
			handlers:  nil,
			options: []AgentOption{
				WithCustomSendHeartbeat(nil),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    DefaultHTTPClient,
		},
		{
			name:      "Custom SendHeartbeat",
			id:        "SendHeartbeat id",
			token:     "SendHeartbeat token",
			serverURL: "SendHeartbeat url",
			handlers:  nil,
			options: []AgentOption{
				WithCustomSendHeartbeat(
					func() error {
						return nil
					}),
			},
			wantHandlers:  defaultHandlers,
			wantHeartbeat: DefaultHeartbeatInterval,
			wantRetry:     DefaultRetryInterval,
			wantClient:    DefaultHTTPClient,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agent := NewAgent(tc.id, tc.token, tc.serverURL, tc.handlers, tc.options...)

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

func createTestServer(t *testing.T, wantExecutions, statusCode int, data string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(statusCode)

		for i := 0; i < wantExecutions; i++ {
			_, _ = w.Write([]byte("data: " + data + "\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
}

func TestConnectAndReceive(t *testing.T) {
	testData := `{"command":"TestCommand","params":{"key":"value"}}`
	tests := []struct {
		name           string
		serverURL      string
		data           string
		executions     int
		wantExecutions int
		statusCode     int
		wantErr        bool
	}{
		{
			name:           "default",
			data:           testData,
			executions:     3,
			wantExecutions: 3,
			statusCode:     http.StatusOK,
			wantErr:        false,
		},
		{
			name:           "invalid server URL",
			serverURL:      ":",
			executions:     3,
			wantExecutions: 0,
			wantErr:        true,
		},
		{
			name:           "invalid send",
			serverURL:      "invalid",
			executions:     3,
			wantExecutions: 0,
			wantErr:        true,
		},
		{
			name:           "invalid status",
			executions:     3,
			wantExecutions: 0,
			statusCode:     http.StatusInternalServerError,
			wantErr:        true,
		},
		{
			name:           "invalid data",
			data:           "",
			executions:     1,
			wantExecutions: 0,
			statusCode:     http.StatusOK,
			wantErr:        false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.serverURL == "" {
				server := createTestServer(
					t, tc.executions, tc.statusCode, tc.data)
				defer server.Close()

				tc.serverURL = server.URL
			}

			gotExecutions := 0
			agent := NewAgent("id", "token", tc.serverURL,
				CmdHandlerFuncMap{
					"TestCommand": func(params any) {
						gotExecutions++

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
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if gotExecutions != tc.wantExecutions {
				t.Errorf("got Executions %d, want %d", gotExecutions, tc.wantExecutions)
			}
		})
	}
}

func TestConnectAndReceiveRetry(t *testing.T) {
	ctxTimeout := 250 * time.Millisecond
	retryInterval := 50 * time.Millisecond

	tests := []struct {
		name           string
		connectFn      func(ctx context.Context) error
		wantRetryCount int
	}{
		{
			name: "Successful first attempt",
			connectFn: func(ctx context.Context) error {
				<-ctx.Done() // Keeps running until canceled
				return nil
			},
			wantRetryCount: 0,
		},
		{
			name: "Fail once",
			connectFn: func() func(ctx context.Context) error {
				attempts := 0
				return func(ctx context.Context) error {
					if attempts < 1 {
						attempts++
						return errors.New("failure")
					}
					<-ctx.Done()
					return nil
				}
			}(),
			wantRetryCount: 1,
		},
		{
			name: "Fail twice",
			connectFn: func() func(ctx context.Context) error {
				attempts := 0
				return func(ctx context.Context) error {
					if attempts < 2 {
						attempts++
						return errors.New("failure")
					}
					<-ctx.Done()
					return nil
				}
			}(),
			wantRetryCount: 2,
		},
		{
			name: "Fail forever",
			connectFn: func(ctx context.Context) error {
				return errors.New("failure")
			},
			wantRetryCount: int(ctxTimeout/retryInterval) - 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(
				context.Background(), ctxTimeout)
			defer cancel()

			retryCount := -1
			agent := NewAgent("id", "token", "url", nil,
				WithCustomConnectAndReceive(func(ctx context.Context) error {
					retryCount++
					return tc.connectFn(ctx)
				}),
				WithRetryInterval(retryInterval),
			)

			agent.ConnectAndReceiveRetry(ctx)

			if retryCount != tc.wantRetryCount {
				t.Errorf("got %d retries, want %d",
					retryCount, tc.wantRetryCount)
			}
		})
	}
}

func TestSendHeartbeat(t *testing.T) {
	tests := []struct {
		name           string
		serverHandler  http.HandlerFunc
		wantErr        bool
		wantStatusCode int
	}{
		{
			name: "Successful heartbeat",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(http.StatusOK)
			},
			wantErr:        false,
			wantStatusCode: http.StatusOK,
		},
		{
			name:           "Invalid URL",
			wantErr:        true,
			wantStatusCode: 0,
		},
		{
			name: "Unauthorized request",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantErr:        true,
			wantStatusCode: http.StatusUnauthorized,
		},
		{
			name: "Server error response",
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr:        true,
			wantStatusCode: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var serverURL string
			var client *http.Client

			if tc.serverHandler != nil {
				server := httptest.NewServer(tc.serverHandler)
				defer server.Close()
				serverURL = server.URL
				client = server.Client()
			} else {
				serverURL = "http://invalid-url"
				client = &http.Client{}
			}

			agent := NewAgent("id", "test-token", serverURL, nil,
				WithClient(client),
			)

			err := agent.SendHeartbeat()
			if (err != nil) != tc.wantErr {
				t.Errorf("SendHeartbeat() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestStartHeartbeat(t *testing.T) {
	tests := []struct {
		name          string
		heartbeatFail bool
		wantMinRuns   int
	}{
		{
			name:          "Success",
			heartbeatFail: false,
			wantMinRuns:   2,
		},
		{
			name:          "Failure",
			heartbeatFail: true,
			wantMinRuns:   2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(
				context.Background(), 200*time.Millisecond)
			defer cancel()

			var runCount int32
			heartbeatFunc := func() error {
				atomic.AddInt32(&runCount, 1)
				if tc.heartbeatFail {
					return fmt.Errorf("heartbeat failure")
				}
				return nil
			}

			agent := NewAgent("id", "token", "url", nil,
				WithHeartbeatInterval(50*time.Millisecond),
				WithCustomSendHeartbeat(heartbeatFunc),
			)

			go agent.StartHeartbeat(ctx)
			time.Sleep(300 * time.Millisecond)

			if atomic.LoadInt32(&runCount) < int32(tc.wantMinRuns) {
				t.Errorf("wanted at least %d heartbeats, got %d", tc.wantMinRuns, runCount)
			}
		})
	}
}

func TestValidateMessage(t *testing.T) {
	tests := []struct {
		name    string
		message Message
		wantErr bool
	}{
		{
			name:    "Valid message",
			message: Message{Command: "TestCommand", Params: map[string]any{"key": "value"}},
			wantErr: false,
		},
		{
			name:    "Missing command",
			message: Message{Command: "", Params: map[string]any{"key": "value"}},
			wantErr: true,
		},
		{
			name:    "Invalid parameter format",
			message: Message{Command: "TestCommand", Params: "invalid"},
			wantErr: true,
		},
		{
			name:    "Empty parameters",
			message: Message{Command: "TestCommand", Params: map[string]any{}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMessage(tt.message)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestProcessServerEvent(t *testing.T) {
	tests := []struct {
		name       string
		eventLine  string
		wantErr    bool
		wantParams any
	}{
		{
			name:       "Valid event",
			eventLine:  "data: {\"command\":\"TestCommand\",\"params\":{\"key\":\"value\"}}",
			wantErr:    false,
			wantParams: map[string]any{"key": "value"},
		},
		{
			name:      "Invalid JSON",
			eventLine: "data: {\"command\":\"TestCommand\",\"params\":\"invalid\"",
			wantErr:   true,
		},
		{
			name:      "Missing command",
			eventLine: "data: {\"params\":{\"key\":\"value\"}}",
			wantErr:   true,
		},
		{
			name:      "Invalid parameter format",
			eventLine: "data: {\"command\":\"TestCommand\",\"params\":123}",
			wantErr:   true,
		},
		{
			name:      "Unknown command",
			eventLine: "data: {\"command\":\"UnknownCommand\",\"params\":{}}",
			wantErr:   false, // Unknown commands are ignored, not errors
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotParams any
			agent := NewAgent("id", "token", "url",
				map[string]CommandHandlerFunc{
					"TestCommand": func(params any) { gotParams = params },
				},
			)

			err := agent.processServerEvent(tc.eventLine)
			if (err != nil) != tc.wantErr {
				t.Errorf("processServerEvent() error = %v, wantErr %v", err, tc.wantErr)
			}

			if !reflect.DeepEqual(gotParams, tc.wantParams) {
				t.Errorf("gotParams = %v, want %v", gotParams, tc.wantParams)
			}
		})
	}
}

// mockHTTPTransport implements http.RoundTripper for custom response handling.
type mockHTTPTransport struct {
	doFunc func(req *http.Request) (*http.Response, error)
}

// RoundTrip executes the mocked HTTP request.
func (m *mockHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.doFunc(req)
}

func TestSendRequest(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		endpoint     string
		statusCode   int
		mockError    error
		expectErr    bool
		expectStatus int
	}{
		{
			name:         "Successful GET request",
			method:       "GET",
			endpoint:     "/events",
			statusCode:   http.StatusOK,
			expectErr:    false,
			expectStatus: http.StatusOK,
		},
		{
			name:         "Successful POST request",
			method:       "POST",
			endpoint:     "/heartbeat",
			statusCode:   http.StatusOK,
			expectErr:    false,
			expectStatus: http.StatusOK,
		},
		{
			name:         "Server returns 500",
			method:       "GET",
			endpoint:     "/fail",
			statusCode:   http.StatusInternalServerError,
			expectErr:    true,
			expectStatus: http.StatusInternalServerError,
		},
		{
			name:      "Network failure",
			method:    "GET",
			endpoint:  "/network-error",
			mockError: errors.New("network error"),
			expectErr: true,
		},
		{
			name:       "Invalid method",
			method:     "INVALID",
			endpoint:   "/invalid",
			statusCode: http.StatusBadRequest,
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock HTTP server to return predefined responses.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.endpoint {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			// Create agent with mock client
			agent := NewAgent("id", "test-token", server.URL, nil)

			// Use a mock client if needed
			if tt.mockError != nil {
				agent.Client = &http.Client{
					Transport: &mockHTTPTransport{
						doFunc: func(req *http.Request) (*http.Response, error) {
							return nil, tt.mockError
						},
					},
				}
			} else {
				agent.Client = server.Client()
			}

			// Call sendRequest
			resp, err := agent.sendRequest(context.Background(), tt.method, tt.endpoint)

			// Validate results
			if tt.expectErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if resp.StatusCode != tt.expectStatus {
					t.Errorf("Expected status %d, got %d", tt.expectStatus, resp.StatusCode)
				}
			}
		})
	}
}
