"""LangGraph Supervisor with live MCP discovery and invocation."""
from __future__ import annotations

import json
import os
from typing import Any

from fastapi import FastAPI, Header, HTTPException
from langchain_core.tools import StructuredTool
from langchain_openai import ChatOpenAI
from langgraph.graph import END, START, MessagesState, StateGraph
from langgraph.prebuilt import ToolNode, tools_condition
from mcp import Client
from pydantic import Field, create_model

app = FastAPI(title="Agent Supervisor", version="0.2.0")


def server_config() -> dict[str, dict[str, Any]]:
    raw = os.getenv("MCP_SERVERS", "{}")
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise RuntimeError("MCP_SERVERS must be a JSON object") from exc
    if not isinstance(value, dict):
        raise RuntimeError("MCP_SERVERS must be a JSON object")
    return value


def authorize(token: str | None) -> None:
    expected = os.getenv("SUPERVISOR_TOKEN", "")
    if not expected:
        raise HTTPException(status_code=503, detail="SUPERVISOR_TOKEN is not configured")
    if token != f"Bearer {expected}":
        raise HTTPException(status_code=401, detail="unauthorized")


def public_name(server: str, tool: str) -> str:
    return f"mcp_{server}_{tool}"


def schema_of(tool: Any) -> dict[str, Any]:
    return getattr(tool, "input_schema", None) or getattr(tool, "inputSchema", None) or {"type": "object", "properties": {}}


async def discover_tools() -> list[dict[str, Any]]:
    catalog: list[dict[str, Any]] = []
    for server, config in server_config().items():
        url = config.get("url")
        if not url:
            continue
        async with Client(url) as client:
            page = await client.list_tools()
            for tool in page.tools:
                schema = schema_of(tool)
                catalog.append({
                    "name": public_name(server, tool.name),
                    "server": server,
                    "remote_name": tool.name,
                    "description": tool.description or f"MCP tool {tool.name} from {server}",
                    "input_schema": schema,
                })
    return catalog


def python_type(json_type: str) -> Any:
    return {"string": str, "integer": int, "number": float, "boolean": bool, "object": dict, "array": list}.get(json_type, Any)


def make_langchain_tool(item: dict[str, Any]) -> StructuredTool:
    schema = item["input_schema"]
    properties = schema.get("properties", {})
    required = set(schema.get("required", []))
    fields: dict[str, tuple[Any, Any]] = {}
    for key, spec in properties.items():
        default = ... if key in required else spec.get("default", None)
        fields[key] = (python_type(spec.get("type", "")), Field(default, description=spec.get("description", "")))
    args_model = create_model(f"Args_{item['name']}", **fields)

    async def invoke(**arguments: Any) -> Any:
        config = server_config().get(item["server"])
        if not config or not config.get("url"):
            raise RuntimeError(f"MCP server {item['server']} is not configured")
        async with Client(config["url"]) as client:
            result = await client.call_tool(item["remote_name"], arguments)
        if result.is_error:
            raise RuntimeError("MCP tool returned an error")
        if result.structured_content is not None:
            return result.structured_content
        return "\n".join(getattr(block, "text", "") for block in result.content)

    return StructuredTool.from_function(
        name=item["name"], description=item["description"], coroutine=invoke, args_schema=args_model
    )


async def run_graph(messages: list[dict[str, Any]]) -> str:
    tools = [make_langchain_tool(item) for item in await discover_tools()]
    model = ChatOpenAI(
        model=os.getenv("AGENT_MODEL", "gpt-4o-mini"),
        base_url=os.getenv("LLM_BASE_URL") or None,
        api_key=os.getenv("LLM_API_KEY") or "missing-api-key",
        temperature=0,
    )
    model_with_tools = model.bind_tools(tools) if tools else model

    async def assistant(state: MessagesState) -> dict[str, list[Any]]:
        response = await model_with_tools.ainvoke(state["messages"])
        return {"messages": [response]}

    graph = StateGraph(MessagesState)
    graph.add_node("assistant", assistant)
    graph.add_edge(START, "assistant")
    if tools:
        graph.add_node("tools", ToolNode(tools))
        graph.add_conditional_edges("assistant", tools_condition, {"tools": "tools", END: END})
        graph.add_edge("tools", "assistant")
    else:
        graph.add_edge("assistant", END)
    result = await graph.compile().ainvoke({"messages": messages}, {"recursion_limit": 18})
    final = result["messages"][-1]
    return final.content if isinstance(final.content, str) else json.dumps(final.content, ensure_ascii=False)


@app.get("/healthz")
async def healthz():
    return {"status": "ok", "orchestrator": "langgraph"}


@app.get("/internal/mcp/tools")
async def mcp_tools(authorization: str | None = Header(default=None)):
    authorize(authorization)
    try:
        return await discover_tools()
    except Exception as exc:
        raise HTTPException(status_code=502, detail=f"MCP discovery failed: {exc}") from exc


@app.post("/internal/mcp/tools/call")
async def call_mcp_tool(payload: dict[str, Any], authorization: str | None = Header(default=None)):
    authorize(authorization)
    name = payload.get("name", "")
    arguments = payload.get("arguments", {})
    for item in await discover_tools():
        if item["name"] == name:
            try:
                tool = make_langchain_tool(item)
                return {"result": await tool.ainvoke(arguments)}
            except Exception as exc:
                raise HTTPException(status_code=502, detail="MCP tool call failed") from exc
    raise HTTPException(status_code=404, detail="MCP tool not found")


@app.post("/v1/plan")
async def plan(payload: dict[str, Any], authorization: str | None = Header(default=None)):
    authorize(authorization)
    messages = payload.get("messages")
    if not isinstance(messages, list) or not messages:
        raise HTTPException(status_code=400, detail="messages must be a non-empty array")
    try:
        return {"output": await run_graph(messages)}
    except Exception as exc:
        raise HTTPException(status_code=502, detail=f"agent execution failed: {exc}") from exc
