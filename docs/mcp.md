# Deploying bibcheck as an MCP server

`bibcheck mcp` runs bibcheck as a [Model Context Protocol](https://modelcontextprotocol.io)
server on stdin/stdout, so MCP clients (Claude Code, Claude Desktop, IDE
agents, ...) can check bibliographies as a tool call instead of shelling out to
the CLI.

## Tools

| Tool | Input | What it does |
|------|-------|--------------|
| `check_bibliography` | `pdf_path` or `pdf_base64`, optional `entry`, optional `concurrency` | Extracts the bibliography from a PDF and verifies every entry (or just `entry`) against doi.org, OSTI, arXiv, Crossref, Elsevier, and linked online resources. |
| `check_entry` | `text` | Verifies a single citation string without a PDF. |

Both tools return a structured JSON document: per-entry `summary_state`
(`ok`, `review`, `error`, or `unknown`), the summarizer's comment, and the
status of each source lookup. Entries are analyzed concurrently with the same
bounded worker pool as the CLI.

## Configuration

The server reads the same configuration as the CLI, from the environment:

| Variable | Purpose |
|----------|---------|
| `SHIRTY_API_KEY` | Enables the Shirty LLM pipeline (preferred when both are set) |
| `OPENROUTER_API_KEY` | Enables the OpenRouter LLM pipeline |
| `SHIRTY_BASE_URL` / `OPENROUTER_BASE_URL` | Override provider endpoints |
| `ELSEVIER_API_KEY` | Enables Elsevier Scopus search (optional) |
| `BIBCHECK_CONCURRENCY` | Default per-request entry concurrency (default 4) |
| `OPENAI_AUDIT_ENABLED` / `OPENAI_AUDIT_DIR` | LLM request audit logging |

At least one LLM provider key is required; `bibcheck mcp` exits immediately
with an error if none is configured. Diagnostics are written to stderr only —
stdout carries the MCP protocol.

A tool call may pass `concurrency` to override the default for that request;
lower it when the configured provider enforces strict rate limits.

## Claude Code (project `.mcp.json`)

```json
{
  "mcpServers": {
    "bibcheck": {
      "command": "bibcheck",
      "args": ["mcp"],
      "env": {
        "SHIRTY_API_KEY": "${SHIRTY_API_KEY}"
      }
    }
  }
}
```

Or register it from the command line:

```bash
claude mcp add bibcheck -e SHIRTY_API_KEY=$SHIRTY_API_KEY -- bibcheck mcp
```

## Claude Desktop

Add to `claude_desktop_config.json` (Settings → Developer → Edit Config):

```json
{
  "mcpServers": {
    "bibcheck": {
      "command": "/usr/local/bin/bibcheck",
      "args": ["mcp"],
      "env": {
        "SHIRTY_API_KEY": "sk-..."
      }
    }
  }
}
```

Since Claude Desktop has no shared shell environment, put the key directly in
the `env` block. Use `pdf_base64` if the client and server do not share a
filesystem.

## Docker

The standard image already contains the binary; override the command:

```bash
docker build -f bare.Dockerfile -t bibcheck .
docker run -i --rm \
  -e SHIRTY_API_KEY \
  -v "$PWD/papers:/papers:ro" \
  bibcheck bibcheck mcp
```

`-i` (and no `-t`) is required: MCP over stdio needs a plain pipe, not a TTY.
Mount the directory containing your PDFs and pass container paths (e.g.
`/papers/mypaper.pdf`) as `pdf_path`, or avoid the mount entirely by sending
`pdf_base64`.

An MCP client config that launches the container per session:

```json
{
  "mcpServers": {
    "bibcheck": {
      "command": "docker",
      "args": [
        "run", "-i", "--rm",
        "-e", "SHIRTY_API_KEY",
        "-v", "/home/me/papers:/papers:ro",
        "bibcheck", "bibcheck", "mcp"
      ]
    }
  }
}
```

## Operational notes

* **Concurrency & rate limits.** Each `check_bibliography` call fans entries
  out across at most `concurrency` workers (default `BIBCHECK_CONCURRENCY`,
  default 4). Every entry costs several LLM calls plus metadata-API lookups;
  size the bound to the strictest upstream rate limit.
* **Audit logs.** LLM request auditing (`OPENAI_AUDIT_DIR`) is safe under
  concurrency; each request is written to a uniquely named file.
* **One session per process.** The stdio server serves the client that
  spawned it and exits when stdin closes; supervisors and MCP clients should
  start one process per session.
