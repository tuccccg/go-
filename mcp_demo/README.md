# Local MCP demo server

This local-only server exposes three Agent tools over the MCP Streamable HTTP transport. It does not call an external service.

- `calculate`: add, subtract, multiply, or divide two numbers.
- `get_current_time`: return the current time in an IANA timezone.
- `search_knowledge_base`: search Markdown and text notes under `data/knowledge/`.

Start it from the repository root with:

```powershell
supervisor\.venv\Scripts\python.exe mcp_demo\server.py
```

The endpoint is `http://127.0.0.1:8002/mcp`.

The root `.env` registers this endpoint as the `demo` MCP server. The Go gateway imports the discovered tools from the Supervisor.
