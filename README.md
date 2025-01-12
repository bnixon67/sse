# SSE (Server-Sent Events) Library

`sse` is a Go package for managing Server-Sent Events (SSE) connections. It provides tools for managing clients, handling event-driven communication, sending commands, and maintaining active connections with heartbeats and cleanup routines.

---

## Features

- **Agent Management**: Manage multiple clients (agents) with unique IDs.
- **Event Handling**: Process server-sent events and execute custom command handlers.
- **Heartbeat Mechanism**: Keep connections alive with periodic heartbeats.
- **Automatic Reconnection**: Retry connections with exponential backoff.
- **Thread-Safe Operations**: Concurrency-safe management of clients.
- **Cleanup Routines**: Automatically clean up disconnected clients.
