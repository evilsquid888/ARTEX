# ARTEX terminal operations

The TUI is a separate Go client of the existing ARTEX HTTP API. ARTEX keeps
running when you quit the client or lose SSH. It does not start another engine.

## Build and connect

From the repository root, using the Go version in `go.mod`:

```bash
go build -o artex-tui ./cmd/artex-tui
```

Open WezTerm, SSH to the host running ARTEX, and run the binary there:

```bash
ssh your-host
cd /path/to/ARTEX
./artex-tui --login
```

Enter the existing ARTEX admin password at the hidden prompt. Login tokens stay
in memory. Initialize ARTEX through the web UI first if it is a new installation.
The default API address is `http://127.0.0.1:8787`; use `--server` for another
address. For Docker, use the host's published ARTEX port.

For an existing session token, use `--token-file /path/to/token` or `ARTEX_TOKEN`.
Treat token files as credentials and restrict their permissions (mode 600).
`--login` overrides a supplied token. Expired tokens require another login.

You can also run the binary locally through an SSH tunnel:

```bash
ssh -N -L 18787:127.0.0.1:8787 your-host
# In another terminal:
./artex-tui --server http://127.0.0.1:18787 --login
```

## Navigation

| Key | Action |
| --- | --- |
| `[` / `]` | Previous / next task |
| `1`–`5`, `Tab`, `Shift+Tab` | Activity, Workers, Chat, Findings, Logs |
| `j` / `k`, arrows | Select an entry |
| `Enter` | Expand activity/tool details; open a worker transcript; inspect a finding |
| `PgUp` / `PgDn` | Scroll |
| `g` / `G` | First / last entry |
| `b` | Load older main-agent or selected worker history |
| `/` | Filter current entries; Enter applies |
| `c` | Compose a message to the selected task's main agent |
| `w` | On Workers, compose a reply to the selected paused worker |
| `Enter` in composer | Send the message |
| `Esc` | Cancel composer, or clear detail / worker / filter |
| `r` | Refresh task data or retry a failed initial snapshot |
| `q`, `Ctrl+C` | Quit the client; ARTEX continues running |

Chat is actual agent input, and can affect task execution. The Chat tab filters
activity to the main agent; its replies and tool use appear live. Worker replies
use the existing paused-worker continuation API and resume that worker. The server
enforces lifecycle restrictions, including a paused or finished parent task.
Inherited workers are viewable, but cannot receive messages.

## Live updates and limits

Task activity uses SSE with a sequence cursor, duplicate suppression, and
reconnect backoff. Task summaries, worker lists, findings, and global backend logs
refresh every five seconds. Logs are global, even while a task is selected.
The first activity request loads 300 events, then SSE catches up from that cursor.
The live buffer retains the most recent 2,000 events; use `b` for older session
history. Worker lists show up to 300 workers and findings up to 200 per task.
Findings expand the evidence available in the list response; report editing,
attachments, task controls, and approval decisions remain in the web interface.
The terminal must be at least 50 columns by 18 rows; 100 by 30 is more comfortable.

Messages are never automatically retried. After a network timeout, inspect the
transcript before sending again: the server may already have accepted the turn.

## Validation

```bash
go test ./internal/tui ./cmd/artex-tui
go vet ./internal/tui ./cmd/artex-tui
```

Tests use a local HTTP/SSE server to verify authentication, message destinations,
stream replay, duplicate suppression, cancellation, and redirect handling, plus
model tests for worker lifecycle checks and terminal rendering.
