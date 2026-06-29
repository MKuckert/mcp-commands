from mcp.server.lowlevel import Server
from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp.types import (
    ErrorData,
    TextContent,
    Tool,
    Annotations,
    Field,
    Annotated,
    INVALID_PARAMS,
)
from pydantic import BaseModel
import subprocess
import os, json
from mcp.shared.exceptions import McpError


class Folder(BaseModel):
    """Parameters"""
    folder: Annotated[str, Field(description="The full path of the current folder that the Android project sits")]

server = Server("build")

@server.list_tools()
async def list_tools() -> list[Tool]:
    return [
        Tool(
            name = "build",
            description = "Build the Android project in the folder",
            inputSchema = Folder.model_json_schema(),
        ),
        Tool(
            name="test",
            description="Run test for the Android project in the folder",
            inputSchema=Folder.model_json_schema(),
        ),
        Tool(
            name="instrumentedTest",
            description="Run instrumented test for the Android project in the folder",
            inputSchema=Folder.model_json_schema(),
        )
    ]
@server.call_tool()
async def call_tool(name, arguments: dict) -> list[TextContent]:
    try:
        args = Folder(**arguments)
    except ValueError as e:
        raise McpError(ErrorData(code=INVALID_PARAMS, message=str(e)))
    # os.chdir(args.folder)
    script_dir = os.path.dirname(os.path.abspath(__file__))

    command = [""]
    if name == "build":
        command = [os.path.join(script_dir, "build.sh"), args.folder]
    elif name == "test":
        command = [os.path.join(script_dir, "test.sh"), args.folder]
    else:
        command = [os.path.join(script_dir, "instrumentedTest.sh"), args.folder]

    result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    stdout_lines = result.stdout.decode("utf-8").splitlines()
    stderr_lines = result.stderr.decode("utf-8").splitlines()
    all_lines = stdout_lines + stderr_lines
    
    
    error_lines = [line for line in all_lines if "failure: " in line.lower() or "e: " in line.lower() or " failed" in line.lower()]
    error_message = "\n".join(error_lines)
    if not error_message:
        error_message = "Successful"
    return [
        TextContent(type="text", text=f"{error_message}")
        ]


async def run():
    options = server.create_initialization_options()
    async with stdio_server() as (read_stream, write_stream):
        await server.run(
            read_stream,
            write_stream,
            options,
            raise_exceptions=True,
        )

if __name__ == "__main__":
    import asyncio
    asyncio.run(run())
