import { appendFileSync } from "node:fs";
import { createServer } from "node:http";

const requestRecord = process.env.P23_MODEL_REQUEST_RECORD;
const forceErrorMatch = process.env.P23_MODEL_FORCE_ERROR_MATCH;
let responseSerial = 0;

function sendSSE(response, event) {
  response.write(`event: ${event.type}\n`);
  response.write(`data: ${JSON.stringify(event)}\n\n`);
}

function completedResponse(id, output) {
  return {
    id,
    object: "response",
    created_at: Math.floor(Date.now() / 1000),
    status: "completed",
    model: "p23-zed-model",
    output,
    parallel_tool_calls: true,
    tools: [],
    usage: {
      input_tokens: 1,
      input_tokens_details: { cached_tokens: 0 },
      output_tokens: 1,
      output_tokens_details: { reasoning_tokens: 0 },
      total_tokens: 2,
    },
  };
}

function emitToolCall(response, toolName, value) {
  const responseID = `resp_${++responseSerial}`;
  const callID = `call_${responseSerial}`;
  const item = {
    id: `fc_${responseSerial}`,
    type: "function_call",
    call_id: callID,
    name: toolName,
    arguments: JSON.stringify({ value }),
    status: "completed",
  };
  sendSSE(response, {
    type: "response.output_item.added",
    sequence_number: 0,
    output_index: 0,
    item: { ...item, status: "in_progress" },
  });
  sendSSE(response, {
    type: "response.output_item.done",
    sequence_number: 1,
    output_index: 0,
    item,
  });
  sendSSE(response, {
    type: "response.completed",
    sequence_number: 2,
    response: completedResponse(responseID, [item]),
  });
  response.end("data: [DONE]\n\n");
}

function emitText(response, text) {
  const responseID = `resp_${++responseSerial}`;
  const itemID = `msg_${responseSerial}`;
  const part = { type: "output_text", text, annotations: [], logprobs: [] };
  const item = {
    id: itemID,
    type: "message",
    role: "assistant",
    status: "completed",
    content: [part],
  };
  sendSSE(response, {
    type: "response.output_item.added",
    sequence_number: 0,
    output_index: 0,
    item: { ...item, status: "in_progress", content: [] },
  });
  sendSSE(response, {
    type: "response.content_part.added",
    sequence_number: 1,
    output_index: 0,
    content_index: 0,
    item_id: itemID,
    part: { ...part, text: "" },
  });
  sendSSE(response, {
    type: "response.output_text.delta",
    sequence_number: 2,
    output_index: 0,
    content_index: 0,
    item_id: itemID,
    delta: text,
    logprobs: [],
  });
  sendSSE(response, {
    type: "response.content_part.done",
    sequence_number: 3,
    output_index: 0,
    content_index: 0,
    item_id: itemID,
    part,
  });
  sendSSE(response, {
    type: "response.output_item.done",
    sequence_number: 4,
    output_index: 0,
    item,
  });
  sendSSE(response, {
    type: "response.completed",
    sequence_number: 5,
    response: completedResponse(responseID, [item]),
  });
  response.end("data: [DONE]\n\n");
}

const server = createServer(async (request, response) => {
  if (request.method !== "POST" || !request.url?.endsWith("/responses")) {
    response.writeHead(404).end();
    return;
  }
  const chunks = [];
  for await (const chunk of request) {
    chunks.push(chunk);
  }
  const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  const tools = Array.isArray(body.tools) ? body.tools : [];
  const input = Array.isArray(body.input) ? body.input : [];
  if (requestRecord) {
    appendFileSync(
      requestRecord,
      `${JSON.stringify({
        toolNames: tools.map((tool) => tool?.name ?? tool?.type),
        lastInputType: input.at(-1)?.type ?? null,
        lastInputRole: input.at(-1)?.role ?? null,
      })}\n`,
    );
  }
  if (forceErrorMatch && JSON.stringify(input).includes(forceErrorMatch)) {
    response.writeHead(500, { "content-type": "application/json" });
    response.end(
      JSON.stringify({
        error: {
          message: "intentional ACP smoke failure",
          type: "server_error",
        },
      }),
    );
    return;
  }

  response.writeHead(200, {
    "content-type": "text/event-stream",
    "cache-control": "no-cache",
    connection: "keep-alive",
  });
  if (tools.length === 0) {
    emitText(response, "[]");
    return;
  }
  if (input.at(-1)?.type === "function_call_output") {
    emitText(response, "zed interop complete");
    return;
  }
  const userText = input
    .filter((item) => item?.role === "user" && typeof item.content === "string")
    .map((item) => item.content)
    .join("\n");
  const requestedTool = userText.includes("shutdown")
    ? tools.find((tool) => tool?.name?.endsWith("__shutdown"))
    : tools.find((tool) => tool?.name?.endsWith("__echo"));
  if (!requestedTool) {
    emitText(response, "requested MCP tool unavailable");
    return;
  }
  emitToolCall(
    response,
    requestedTool.name,
    requestedTool.name.endsWith("__echo") ? "zed-v1" : "",
  );
});

await new Promise((resolveListen, rejectListen) => {
  server.once("error", rejectListen);
  server.listen(0, "127.0.0.1", resolveListen);
});
const address = server.address();
if (!address || typeof address !== "object") {
  throw new Error("fake model server did not bind");
}
process.stdout.write(`http://127.0.0.1:${address.port}/v1\n`);

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
