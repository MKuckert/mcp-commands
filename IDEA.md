# MCP CLI-Bridge

## 1. Project Overview

The **MCP CLI-Bridge** is an MCP (Model Context Protocol) server designed to dynamically load other commands and expose them as structured tools to LLMs. It allows controlled execution of CLI programs within a specific target directory, supporting hot-reloading and robust parameter mapping.

## 2. CLI Interface & Execution Flow

The server application must handle the following command-line arguments:

- `--dir <path>`: The target working directory (CWD) where tools will be executed.
- `--scripts <path>`: The folder containing executable commands.
- `--watch`: (Flag) Enables a `FileSystemWatcher` for live tool updates.
- `--ip <address>`: Host binding for http transport mode (Default: `127.0.0.1`).
- `--port <number>`: Port for http transport mode (Default: `5000`).

**Bootstrapping Sequence:**

1. **Start:** Parse arguments.
2. **Discovery:** Scan the `--scripts` folder and perform a "Discovery Run" on all executable files while still in the original execution directory.
3. **Hard Switch:** Change the process CWD to the path provided in `--dir`.
4. **Serve:** Initialize the MCP server (Stdio or http transport, depending on `--ip` and `--port` flags).

## Security & Stability

- **Process Integrity:** Scripts run in separate processes.
- **Timeouts:** Implement a default timeout (e.g., 5-10 minutes) for CLI processes to prevent zombie builds.
