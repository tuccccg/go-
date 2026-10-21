# Python LangGraph Supervisor

This service discovers remote MCP tools from the `MCP_SERVERS` JSON environment variable, builds LangGraph tool loops for `/v1/plan`, and exposes a catalog/call API consumed by the Go gateway. Set `SUPERVISOR_TOKEN`, `LLM_BASE_URL`, `LLM_API_KEY`, and `AGENT_MODEL`. The internal MCP endpoints fail closed when the shared token is missing.

Run locally with `pip install -r requirements.txt` followed by `uvicorn main:app --port 8001`. For a full local deployment, use `docker compose up --build` from the repository root.
