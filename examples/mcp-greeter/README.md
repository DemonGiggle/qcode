# Go MCP greeter

This example exposes one MCP tool over stdio: `greet` takes a `name` and returns a plain-text greeting. It uses the Go MCP SDK and does not access files, run commands, or make network requests.

From the repository root, build the example:

```sh
go build -o ./bin/mcp-greeter ./examples/mcp-greeter
```

Add this entry to your [qcode configuration](../../docs/configuration.md#mcp-servers), replacing the example path with the absolute path to the binary you just built (include `.exe` on Windows):

```toml
[mcp_servers.greeter]
command = "/absolute/path/to/qcode/bin/mcp-greeter"
```

Run `qcode config check`, then start qcode with a configured provider in a normal session. qcode launches the server subprocess automatically; no `--stdio` argument or separate terminal is needed. MCP tools are unavailable in sandbox, plan, and skill-plan modes.

Try asking: **Use the greeter MCP tool to greet Ada.** The tool should return `Hello, Ada!`.
