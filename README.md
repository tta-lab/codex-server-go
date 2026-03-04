# codex-server-go

Go SDK for the Codex app-server WebSocket JSON-RPC protocol.

Types are generated from the [Codex JSON Schema](https://github.com/openai/codex) using a custom codegen script.

## Usage

```go
import "github.com/tta-lab/codex-server-go/protocol"

// Use method constants
method := protocol.MethodThreadStart

// Use typed params
params := protocol.ThreadStartParams{
    Cwd:   &cwd,
    Model: &model,
}

// Unmarshal server notifications
var notif protocol.ServerNotification
json.Unmarshal(data, &notif)

switch notif.Method {
case protocol.NotifItemAgentMessageDelta:
    p, _ := notif.ItemAgentMessageDeltaParams()
    fmt.Println(p.Delta)
}
```

## Development

```bash
# Regenerate types from schema
make generate

# Run all CI checks
make ci
```

## Regenerating

1. Copy the updated schema to `schema/codex_app_server_protocol.schemas.json`
2. Run `make generate`
3. Run `make ci` to verify
