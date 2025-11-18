# HTTP API Example

This example shows how to expose `agentsdk-go` over HTTP using only the Go standard library. It demonstrates:

- `POST /v1/run` for classic, blocking requests.
- `POST /v1/run/stream` for Server-Sent Events (SSE) updates backed by `Runtime.RunStream`.
- `POST /v1/tools/execute` for direct tool execution through the SDK registry.
- Request-scoped sandbox overrides (filesystem, network, resource limits).
- Per-request timeouts and consistent JSON error responses.

## Prerequisites

- Go 1.23+
- An Anthropic API key exported as `ANTHROPIC_API_KEY`.
- Run commands from the module root (`github.com/cexll/agentsdk-go`).

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/http
```

The server listens on `:8080` by default. Use `CTRL+C` to shut it down cleanly.

## Configuration

| Env var | Purpose | Default |
| --- | --- | --- |
| `AGENTSDK_HTTP_ADDR` | Listen address | `:8080` |
| `AGENTSDK_PROJECT_ROOT` | Workspace root exposed to the agent/tools | current working dir |
| `AGENTSDK_SANDBOX_ROOT` | Filesystem sandbox root (falls back to project root) | — |
| `AGENTSDK_MODEL` | Anthropic model name | `claude-3-5-sonnet-20241022` |
| `AGENTSDK_NETWORK_ALLOW` | Comma-separated allow-list for outbound hosts | `api.anthropic.com` |
| `AGENTSDK_DEFAULT_TIMEOUT` | Default request timeout (Go duration or milliseconds) | `45s` |
| `AGENTSDK_MAX_TIMEOUT` | Hard timeout cap | `120s` |
| `AGENTSDK_RESOURCE_CPU_PERCENT` | Default sandbox CPU budget | `85` |
| `AGENTSDK_RESOURCE_MEMORY_MB` | Default memory cap | `1536` |
| `AGENTSDK_RESOURCE_DISK_MB` | Default disk cap | `2048` |
| `AGENTSDK_MAX_BODY_BYTES` | Max JSON body size | `1MiB` |
| `AGENTSDK_MAX_SESSIONS` | LRU cap for in-memory session histories (prevents growth/leaks) | `500` |

`MaxSessions` works with the history store's LRU eviction to avoid unbounded session memory usage and explicitly prevents runaway memory leaks during long-lived deployments.

## Non-streaming runs

```
curl -sS -X POST http://localhost:8080/v1/run \
  -H 'Content-Type: application/json' \
  -d '{
        "prompt": "用一句话解释 agentsdk-go",
        "session_id": "demo-client",
        "metadata": {"customer": "demo"},
        "sandbox": {
          "allowed_paths": ["outputs"],
          "network_allow": ["api.anthropic.com"],
          "resource": {"max_cpu_percent": 80, "max_memory_mb": 1024}
        }
      }'
```

Response fields:

- `output`, `stop_reason`, `usage` mirror `api.Response.Result`.
- `sandbox` echoes the enforced allow-list.
- `tool_calls` lists function calls produced by the model (if any).

Errors return `{ "code": "...", "error": "..." }` with the proper HTTP status.

## Streaming runs (SSE)

`POST /v1/run/stream` provides real-time progress updates via Server-Sent Events with full Anthropic API compatibility.

```bash
curl --no-buffer -N -X POST http://localhost:8080/v1/run/stream \
  -H 'Content-Type: application/json' \
  -d '{"prompt": "列出 examples 目录", "session_id": "stream-demo"}'
```

### Event Types

The endpoint emits **Anthropic-compatible events** with agent-specific extensions:

| Event Type | Description | Fields |
|------------|-------------|--------|
| `agent_start` | Agent session begins | `session_id` |
| `iteration_start` | New agent loop iteration | `iteration`, `session_id` |
| `message_start` | Model starts generating | `message` (metadata) |
| `content_block_start` | Text/tool block begins | `index`, `content_block.type` |
| `content_block_delta` | Incremental text chunk | `index`, `delta.text` |
| `content_block_stop` | Block generation complete | `index` |
| `tool_execution_start` | Tool call initiated | `tool_use_id`, `name`, `params` |
| `tool_execution_stop` | Tool call finished | `tool_use_id`, `output`, `duration_ms` |
| `iteration_stop` | Loop iteration done | `iteration` |
| `agent_stop` | Agent session ends | `total_iterations`, `stop_reason` |
| `message_stop` | Final message metadata | `message` (full response) |
| `ping` | Keep-alive heartbeat | (empty, every 15s) |

### Complete Event Flow Example

```
event: agent_start
data: {"type":"agent_start","session_id":"stream-demo"}

event: iteration_start
data: {"type":"iteration_start","iteration":0,"session_id":"stream-demo"}

event: message_start
data: {"type":"message_start","message":{"id":"msg_123","model":"claude-3-5-sonnet-20241022","role":"assistant"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"会"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"列出"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_456","name":"bash_execute"}}

event: tool_execution_start
data: {"type":"tool_execution_start","tool_use_id":"toolu_456","name":"bash_execute","params":{"command":"ls examples"}}

event: tool_execution_stop
data: {"type":"tool_execution_stop","tool_use_id":"toolu_456","output":"cli\nhttp\nmcp","duration_ms":45}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: iteration_stop
data: {"type":"iteration_stop","iteration":0}

event: agent_stop
data: {"type":"agent_stop","total_iterations":1,"stop_reason":"end_turn"}

event: message_stop
data: {"type":"message_stop","message":{"id":"msg_123","stop_reason":"end_turn","usage":{"input_tokens":120,"output_tokens":45}}}
```

### Implementation Details

- **Based on Progress Middleware**: Events are generated by the 6-point middleware system (`before_agent`, `before_model`, `after_model`, `before_tool`, `after_tool`, `after_agent`).
- **Character-by-character streaming**: `content_block_delta` events stream text incrementally for real-time display.
- **Tool execution visibility**: `tool_execution_start/stop` provide live feedback for bash commands, file operations, etc.
- **Heartbeat**: `ping` events every 15 seconds prevent proxy/CDN timeouts.
- **Standard compliance**: Event structure matches Anthropic Messages API streaming format.

### Error Handling

On error, the stream emits:
```
event: error
data: {"type":"error","error":{"type":"runtime_error","message":"context deadline exceeded"}}
```

The connection then closes. Clients should implement exponential backoff for retries.

## Tool execution endpoint

```
curl -sS -X POST http://localhost:8080/v1/tools/execute \
  -H 'Content-Type: application/json' \
  -d '{
        "name": "bash_execute",
        "params": {"command": "ls -1 examples"},
        "usage": {"cpu_percent": 5},
        "sandbox": {"allowed_paths": ["examples"], "resource": {"max_cpu_percent": 25}}
      }'
```

This dispatches the request through the same tool registry used by the agent. Provide sandbox overrides to scope filesystem/network access per call.

## curl helper

`curl-examples.sh` runs a non-streaming prompt, a streaming prompt, and a tool call against `http://localhost:8080`. Override `BASE_URL` or `PROMPT` as needed:

```bash
BASE_URL=http://127.0.0.1:8080 PROMPT="Describe project" bash examples/http/curl-examples.sh
```

## Sandbox overrides

Every request can tighten policies:

```json
"sandbox": {
  "root": "./tmp",
  "allowed_paths": ["./tmp", "./docs"],
  "network_allow": ["api.anthropic.com"],
  "resource": {
    "max_cpu_percent": 60,
    "max_memory_mb": 512,
    "max_disk_mb": 512
  }
}
```

Values are merged with the server defaults; duplicates are removed automatically.

## Timeout handling

Requests default to `AGENTSDK_DEFAULT_TIMEOUT`. Override per call via `timeout_ms`. The server caps the value to `AGENTSDK_MAX_TIMEOUT` to prevent runaway jobs.

## Development notes

- The example sticks to the standard library for HTTP/SSE to keep dependencies minimal.
- Tool execution reuses the SDK registry so responses match agent-triggered tool calls.
- All JSON decoders enforce a 1MiB limit to guard against accidental large payloads.
