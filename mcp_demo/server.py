"""Tiny local MCP server used to demonstrate agent tool discovery and calls."""
from __future__ import annotations

import asyncio
import re
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from mcp.server.mcpserver import MCPServer


server = MCPServer(name="agent-productivity-tools", version="1.0.0")
KNOWLEDGE_DIR = Path(__file__).resolve().parents[1] / "data" / "knowledge"


@server.tool()
def calculate(operation: str, a: float, b: float) -> float:
    """Calculate a +, -, *, or / operation on two numbers."""
    aliases = {
        "add": "add", "sum": "add", "plus": "add", "addition": "add", "+": "add", "加": "add", "加法": "add",
        "subtract": "subtract", "minus": "subtract", "subtraction": "subtract", "-": "subtract", "减": "subtract", "减法": "subtract",
        "multiply": "multiply", "multiplication": "multiply", "product": "multiply", "times": "multiply", "mul": "multiply", "*": "multiply", "×": "multiply", "x": "multiply", "乘": "multiply", "乘法": "multiply",
        "divide": "divide", "division": "divide", "quotient": "divide", "div": "divide", "/": "divide", "÷": "divide", "除": "divide", "除法": "divide",
    }
    normalized = aliases.get(operation.strip().casefold())
    if normalized is None:
        raise ValueError("operation must be add, subtract, multiply, or divide")
    if normalized == "divide" and b == 0:
        raise ValueError("cannot divide by zero")
    return {
        "add": lambda: a + b,
        "subtract": lambda: a - b,
        "multiply": lambda: a * b,
        "divide": lambda: a / b,
    }[normalized]()


@server.tool()
def get_current_time(timezone: str = "Asia/Shanghai") -> dict[str, str]:
    """Get the current local time for an IANA timezone, such as Asia/Shanghai."""
    try:
        zone = ZoneInfo(timezone)
    except ZoneInfoNotFoundError as exc:
        raise ValueError(f"unknown timezone: {timezone}") from exc
    now = datetime.now(zone)
    return {"timezone": timezone, "iso_time": now.isoformat(), "display": now.strftime("%Y-%m-%d %H:%M:%S %Z")}


@server.tool()
def search_knowledge_base(query: str, limit: int = 5) -> list[dict[str, str]]:
    """Search local project notes and return matching snippets."""
    query = query.strip()
    if not query:
        raise ValueError("query must not be empty")
    limit = max(1, min(limit, 10))
    terms = re.findall(r"[a-z0-9_]+|[\u4e00-\u9fff]+", query.casefold())
    matches: list[tuple[int, str, str]] = []
    for path in KNOWLEDGE_DIR.glob("**/*"):
        if not path.is_file() or path.suffix.lower() not in {".md", ".txt"}:
            continue
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except (OSError, UnicodeError):
            continue
        for line_number, line in enumerate(lines, start=1):
            normalized = line.casefold()
            score = sum(1 for term in terms if term in normalized)
            if query.casefold() in normalized:
                score += 2
            if score:
                matches.append((score, path.name, f"{line_number}: {line.strip()}"))
    matches.sort(key=lambda item: (-item[0], item[1], item[2]))
    return [{"file": name, "snippet": snippet} for _, name, snippet in matches[:limit]]


async def main() -> None:
    await server.run_streamable_http_async(
        host="127.0.0.1",
        port=8002,
        streamable_http_path="/mcp",
        stateless_http=True,
        json_response=True,
    )


if __name__ == "__main__":
    asyncio.run(main())
