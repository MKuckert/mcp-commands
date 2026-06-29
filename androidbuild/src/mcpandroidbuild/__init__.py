from .server import run

def main():
    """Android Project MCP Server - Building Android project"""
    import asyncio
    asyncio.run(run())


if __name__ == "__main__":
    main()