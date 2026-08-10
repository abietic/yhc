import { appendFileSync } from "node:fs";
import { createInterface } from "node:readline";

const launchRecord = process.env.P23_MCP_LAUNCH_RECORD;
const callRecord = process.env.P23_MCP_CALL_RECORD;
const secret = process.env.P23_MCP_SECRET ?? "";

if (!launchRecord || !callRecord) {
  throw new Error("P23 MCP record paths are required");
}

appendFileSync(
  launchRecord,
  `${JSON.stringify({
    pid: process.pid,
    cwd: process.cwd(),
    args: process.argv.slice(2),
    secret,
  })}\n`,
);

function send(message, afterWrite) {
  process.stdout.write(`${JSON.stringify(message)}\n`, afterWrite);
}

function response(id, result) {
  send({ jsonrpc: "2.0", id, result });
}

const lines = createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
  terminal: false,
});

lines.on("line", (line) => {
  let message;
  try {
    message = JSON.parse(line);
  } catch {
    return;
  }

  if (message.id === undefined || message.id === null) {
    return;
  }

  switch (message.method) {
    case "initialize":
      response(message.id, {
        protocolVersion: message.params?.protocolVersion ?? "2025-06-18",
        capabilities: { tools: { listChanged: true } },
        serverInfo: { name: "p23-5-interop-mcp", version: "1.0.0" },
      });
      break;
    case "tools/list":
      response(message.id, {
        tools: [
          {
            name: "echo",
            description: "Return the supplied value.",
            inputSchema: {
              type: "object",
              properties: { value: { type: "string" } },
              required: ["value"],
            },
          },
          {
            name: "shutdown",
            description: "Exit after acknowledging the call.",
            inputSchema: { type: "object", properties: {} },
          },
        ],
      });
      break;
    case "tools/call": {
      const name = message.params?.name ?? "";
      const value = message.params?.arguments?.value ?? "";
      appendFileSync(
        callRecord,
        `${JSON.stringify({ pid: process.pid, name, value })}\n`,
      );
      const text = name === "echo" ? `echo:${value}` : "shutting down";
      send(
        {
          jsonrpc: "2.0",
          id: message.id,
          result: {
            content: [{ type: "text", text }],
            isError: false,
          },
        },
        name === "shutdown" ? () => setTimeout(() => process.exit(0), 20) : undefined,
      );
      break;
    }
    default:
      send({
        jsonrpc: "2.0",
        id: message.id,
        error: { code: -32601, message: "method not found" },
      });
  }
});

lines.on("close", () => process.exit(0));
