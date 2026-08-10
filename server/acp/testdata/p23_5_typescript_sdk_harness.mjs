import { spawn } from "node:child_process";
import {
  appendFile,
  mkdtemp,
  mkdir,
  readFile,
  realpath,
  rm,
  writeFile,
} from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { Readable, Writable } from "node:stream";
import { fileURLToPath, pathToFileURL } from "node:url";

const sdkEntry = process.env.P23_ACP_SDK_ENTRY;
const agentBinary = process.env.P23_AGENT_BINARY;
if (!sdkEntry || !agentBinary) {
  throw new Error("P23_ACP_SDK_ENTRY and P23_AGENT_BINARY are required");
}

const sdkPath = resolve(sdkEntry);
const sdkPackagePath = resolve(dirname(sdkPath), "..", "package.json");
const sdkPackage = JSON.parse(await readFile(sdkPackagePath, "utf8"));
if (
  sdkPackage.name !== "@agentclientprotocol/sdk" ||
  sdkPackage.version !== "1.3.0"
) {
  throw new Error(`unexpected ACP SDK ${sdkPackage.name}@${sdkPackage.version}`);
}
const acp = await import(pathToFileURL(sdkPath).href);

const fixtureDir = dirname(fileURLToPath(import.meta.url));
const mcpHelper = join(fixtureDir, "p23_5_mcp_server.mjs");
const workDir = await mkdtemp(join(tmpdir(), "eino-p23-5-sdk-"));
const projectStateDir = ".yhc";
const resolvedWorkDir = await realpath(workDir);
const launchRecord = join(workDir, "mcp-launches.jsonl");
const callRecord = join(workDir, "mcp-calls.jsonl");
const agentStderr = [];
const modelRequests = [];
const updates = [];
let responseSerial = 0;
const richPNG =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=";

function trace(message) {
  if (process.env.P23_TRACE === "1") {
    process.stderr.write(`[p23-harness] ${message}\n`);
  }
}

trace(`workdir ${workDir}`);

function check(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

async function readJSONLines(path) {
  try {
    return (await readFile(path, "utf8"))
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
  } catch (error) {
    if (error?.code === "ENOENT") {
      return [];
    }
    throw error;
  }
}

async function waitFor(description, predicate, timeoutMs = 8_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await predicate();
    if (value) {
      return value;
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 25));
  }
  throw new Error(`timed out waiting for ${description}`);
}

function processAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error?.code !== "ESRCH";
  }
}

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
    model: "p23-interop-model",
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

function emitProviderRich(response) {
  const responseID = `resp_${++responseSerial}`;
  const firstItemID = `msg_${responseSerial}_first`;
  const reasoningItemID = `rs_${responseSerial}`;
  const secondItemID = `msg_${responseSerial}_second`;
  const firstPart = {
    type: "output_text",
    text: "sdk-provider-rich-one ",
    annotations: [],
    logprobs: [],
  };
  const secondPart = {
    type: "output_text",
    text: "sdk-provider-rich-two",
    annotations: [],
    logprobs: [],
  };
  const firstItem = {
    id: firstItemID,
    type: "message",
    role: "assistant",
    status: "completed",
    content: [firstPart],
  };
  const reasoningItem = {
    id: reasoningItemID,
    type: "reasoning",
    status: "completed",
    summary: [
      { type: "summary_text", text: "sdk-private-reasoning-text" },
    ],
    encrypted_content: "sdk-private-signature-value",
  };
  const secondItem = {
    id: secondItemID,
    type: "message",
    role: "assistant",
    status: "completed",
    content: [secondPart],
  };
  let sequence = 0;
  sendSSE(response, {
    type: "response.output_item.added",
    sequence_number: sequence++,
    output_index: 0,
    item: { ...firstItem, status: "in_progress", content: [] },
  });
  sendSSE(response, {
    type: "response.content_part.added",
    sequence_number: sequence++,
    output_index: 0,
    content_index: 0,
    item_id: firstItemID,
    part: { ...firstPart, text: "" },
  });
  sendSSE(response, {
    type: "response.output_text.delta",
    sequence_number: sequence++,
    output_index: 0,
    content_index: 0,
    item_id: firstItemID,
    delta: firstPart.text,
    logprobs: [],
  });
  sendSSE(response, {
    type: "response.content_part.done",
    sequence_number: sequence++,
    output_index: 0,
    content_index: 0,
    item_id: firstItemID,
    part: firstPart,
  });
  sendSSE(response, {
    type: "response.output_item.done",
    sequence_number: sequence++,
    output_index: 0,
    item: firstItem,
  });
  sendSSE(response, {
    type: "response.output_item.added",
    sequence_number: sequence++,
    output_index: 1,
    item: reasoningItem,
  });
  sendSSE(response, {
    type: "response.output_item.added",
    sequence_number: sequence++,
    output_index: 2,
    item: { ...secondItem, status: "in_progress", content: [] },
  });
  sendSSE(response, {
    type: "response.content_part.added",
    sequence_number: sequence++,
    output_index: 2,
    content_index: 0,
    item_id: secondItemID,
    part: { ...secondPart, text: "" },
  });
  sendSSE(response, {
    type: "response.output_text.delta",
    sequence_number: sequence++,
    output_index: 2,
    content_index: 0,
    item_id: secondItemID,
    delta: secondPart.text,
    logprobs: [],
  });
  sendSSE(response, {
    type: "response.content_part.done",
    sequence_number: sequence++,
    output_index: 2,
    content_index: 0,
    item_id: secondItemID,
    part: secondPart,
  });
  sendSSE(response, {
    type: "response.output_item.done",
    sequence_number: sequence++,
    output_index: 2,
    item: secondItem,
  });
  sendSSE(response, {
    type: "response.completed",
    sequence_number: sequence,
    response: completedResponse(responseID, [
      firstItem,
      reasoningItem,
      secondItem,
    ]),
  });
  response.end("data: [DONE]\n\n");
}

function mixedRichPromptBlocks(before, after) {
  return [
    { type: "text", text: before },
    {
      type: "resource_link",
      uri: "file:///sdk/schema.json",
      name: "schema.json",
      mimeType: "application/json",
      annotations: {
        audience: ["assistant"],
        priority: 0.5,
        _meta: { reserved: "must-not-forward" },
      },
      _meta: { reserved: "must-not-forward" },
    },
    {
      type: "image",
      data: richPNG,
      mimeType: "image/png",
      uri: "file:///sdk/source-image-must-not-forward.png",
      annotations: {
        audience: ["user"],
        _meta: { reserved: "must-not-forward" },
      },
      _meta: { reserved: "must-not-forward" },
    },
    {
      type: "resource",
      resource: {
        uri: "file:///sdk/context.txt",
        mimeType: "text/plain",
        text: "sdk embedded text",
        _meta: { reserved: "must-not-forward" },
      },
      annotations: { audience: ["assistant"] },
      _meta: { reserved: "must-not-forward" },
    },
    {
      type: "resource",
      resource: {
        uri: "file:///sdk/pixel.png",
        mimeType: "image/png",
        blob: richPNG,
        _meta: { reserved: "must-not-forward" },
      },
      _meta: { reserved: "must-not-forward" },
    },
    { type: "text", text: after },
  ];
}

const modelServer = createServer(async (request, response) => {
  if (request.method !== "POST" || !request.url?.endsWith("/responses")) {
    response.writeHead(404).end();
    return;
  }
  const chunks = [];
  for await (const chunk of request) {
    chunks.push(chunk);
  }
  const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  modelRequests.push(body);
  trace(`model request ${modelRequests.length}`);
  trace(
    JSON.stringify({
      tools: Array.isArray(body.tools)
        ? body.tools.map((tool) => tool?.name ?? tool?.type)
        : [],
      lastInputType: Array.isArray(body.input) ? body.input.at(-1)?.type : null,
      lastInputRole: Array.isArray(body.input) ? body.input.at(-1)?.role : null,
    }),
  );
  if (JSON.stringify(body.input).includes("sdk-rich-load-seed-before")) {
    response.writeHead(500, { "content-type": "application/json" });
    response.end(
      JSON.stringify({
        error: {
          message: "intentional rich load seed failure",
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

  const serializedAllInput = JSON.stringify(body.input);
  if (serializedAllInput.includes("sdk-provider-rich-followup-question")) {
    emitText(response, "sdk-provider-rich-continuation-ok");
    return;
  }
  if (serializedAllInput.includes("sdk-provider-rich-seed")) {
    emitProviderRich(response);
    return;
  }

  const tools = Array.isArray(body.tools) ? body.tools : [];
  if (tools.length === 0) {
    emitText(response, "[]");
    return;
  }
  const input = Array.isArray(body.input) ? body.input : [];
  const serializedInput = JSON.stringify(input.slice(-7));
  if (
    serializedInput.includes("sdk-rich-before") ||
    serializedInput.includes("sdk-rich-resume")
  ) {
    emitText(response, "rich interop complete");
    return;
  }
  if (input.at(-1)?.type === "function_call_output") {
    emitText(response, "interop complete");
    return;
  }
  const userText = input
        .filter((item) => item?.role === "user" && typeof item.content === "string")
        .map((item) => item.content)
        .join("\n");
  const requestedTool = userText.includes("shutdown")
    ? tools.find((tool) => tool?.name?.endsWith("__shutdown"))
    : tools.find((tool) => tool?.name?.endsWith("__echo"));
  check(requestedTool, "model request did not expose the requested MCP tool");
  emitToolCall(
    response,
    requestedTool.name,
    requestedTool.name.endsWith("__echo") ? "sdk-v1" : "",
  );
});

await new Promise((resolveListen, rejectListen) => {
  modelServer.once("error", rejectListen);
  modelServer.listen(0, "127.0.0.1", resolveListen);
});
const modelAddress = modelServer.address();
check(
  modelAddress && typeof modelAddress === "object",
  "model server did not bind",
);

const agentHome = join(workDir, "agent-home");
await mkdir(join(agentHome, ".claude"), { recursive: true });
await writeFile(
  join(agentHome, ".claude", "settings.json"),
  JSON.stringify({
    model_profile: "p23-sdk-primary",
    provider_accounts: {
      "p23-sdk-openai": {
        provider: "openai",
        base_url: `http://127.0.0.1:${modelAddress.port}/v1`,
        auth: { kind: "env", name: "P23_ACP_PORTFOLIO_KEY" },
      },
    },
    model_profiles: {
      "p23-sdk-primary": {
        account: "p23-sdk-openai",
        api_model: "gpt-4o",
        metadata: {
          context_window_tokens: 128000,
          max_output_tokens: 16384,
          capabilities: {
            text: true,
            streaming: true,
            tools: true,
            system_prompt: true,
            images: true,
            pdfs: false,
            thinking: true,
          },
        },
      },
    },
  }),
  "utf8",
);

const descriptor = {
  name: "sdk-server",
  command: process.execPath,
  args: [mcpHelper, "--interop-arg"],
  env: [
    { name: "P23_MCP_LAUNCH_RECORD", value: launchRecord },
    { name: "P23_MCP_CALL_RECORD", value: callRecord },
    { name: "P23_MCP_SECRET", value: "p23-private-sdk-value" },
  ],
};

const agent = spawn(
  resolve(agentBinary),
  [
    "serve",
    "acp",
    "--model-profile",
    "p23-sdk-primary",
    "--yolo",
    "--max-turns",
    "4",
  ],
  {
    cwd: workDir,
    env: {
      ...process.env,
      HOME: agentHome,
      P23_ACP_PORTFOLIO_KEY: "p23-sdk-key",
      EINO_AGENT_DISABLE_ACP_ASSISTANT_MESSAGE_IDS: "1",
    },
    stdio: ["pipe", "pipe", "pipe"],
  },
);
agent.stderr.setEncoding("utf8");
agent.stderr.on("data", (chunk) => {
  agentStderr.push(chunk);
  if (process.env.P23_TRACE === "1") {
    process.stderr.write(`[agent] ${chunk}`);
  }
});

const clientApp = acp
  .client({ name: "eino-p23-5-sdk-harness" })
  .onRequest(acp.methods.client.session.requestPermission, ({ params }) => {
    const option = params.options.find((candidate) =>
      candidate.kind.startsWith("allow"),
    );
    return option
      ? { outcome: { outcome: "selected", optionId: option.optionId } }
      : { outcome: { outcome: "cancelled" } };
  })
  .onRequest(acp.methods.client.fs.readTextFile, async ({ params }) => ({
    content: await readFile(params.path, "utf8"),
  }))
  .onRequest(acp.methods.client.fs.writeTextFile, async ({ params }) => {
    await writeFile(params.path, params.content);
    return {};
  })
  .onNotification(acp.methods.client.session.update, ({ params }) => {
    updates.push(params);
  });

const stream = acp.ndJsonStream(
  Writable.toWeb(agent.stdin),
  Readable.toWeb(agent.stdout),
);

let result;
try {
  result = await clientApp.connectWith(stream, async (ctx) => {
    trace("initialize");
    const initialized = await ctx.request(acp.methods.agent.initialize, {
      protocolVersion: 2,
      clientCapabilities: {
        fs: { readTextFile: true, writeTextFile: true },
        terminal: false,
      },
      clientInfo: { name: "p23-5-sdk-harness", version: "1.0.0" },
    });
    check(initialized.protocolVersion === 1, "adapter did not negotiate v1");
    trace(
      `capabilities ${JSON.stringify(initialized.agentCapabilities)}`,
    );
    check(
      initialized.agentCapabilities?.loadSession === true,
      "adapter did not retain loadSession capability",
    );
    check(
      initialized.agentCapabilities?.promptCapabilities?.image === true &&
        initialized.agentCapabilities?.promptCapabilities?.embeddedContext ===
          true &&
        initialized.agentCapabilities?.promptCapabilities?.audio !== true,
      "adapter advertised incorrect rich prompt capabilities",
    );

    trace("session/new");
    const created = await ctx.request(acp.methods.agent.session.new, {
      cwd: workDir,
      mcpServers: [descriptor],
    });
    const firstLaunch = await waitFor("first MCP launch", async () => {
      const launches = await readJSONLines(launchRecord);
      return launches[0];
    });
    check(
      firstLaunch.cwd === resolvedWorkDir,
      "MCP child received the wrong cwd",
    );
    check(
      firstLaunch.args.includes("--interop-arg"),
      "MCP child did not receive exact argv",
    );
    check(
      firstLaunch.secret === "p23-private-sdk-value",
      "MCP child did not receive the descriptor environment",
    );

    trace("rich prompt");
    const richRequestStart = modelRequests.length;
    const richPrompt = await ctx.request(acp.methods.agent.session.prompt, {
      sessionId: created.sessionId,
      prompt: mixedRichPromptBlocks("sdk-rich-before", "sdk-rich-after"),
    });
    check(
      richPrompt.stopReason === "end_turn",
      "rich prompt did not finish",
    );
    check(
      modelRequests.length === richRequestStart + 1,
      "rich prompt did not produce one provider request",
    );
    const richModelRequest = modelRequests.at(-1);
    const richStart = richModelRequest.input.findIndex(
      (item) => item?.role === "user" && item.content === "sdk-rich-before",
    );
    const richProviderItems = richModelRequest.input.slice(
      richStart,
      richStart + 7,
    );
    check(
      richStart >= 0 && richProviderItems.length === 7,
      "rich prompt did not reach the provider as ordered content",
    );
    const richTypes = richProviderItems.map((item) =>
      typeof item?.content === "string"
        ? "input_text"
        : item?.content?.[0]?.type,
    );
    check(
      JSON.stringify(richTypes) ===
        JSON.stringify([
          "input_text",
          "input_text",
          "input_image",
          "input_text",
          "input_text",
          "input_image",
          "input_text",
        ]),
      `unexpected rich provider order ${JSON.stringify(richTypes)}`,
    );
    check(
      richProviderItems[0].content === "sdk-rich-before" &&
        richProviderItems[1].content.includes('"type":"resource_link"') &&
        richProviderItems[3].content.includes('"kind":"text"') &&
        richProviderItems[4].content.includes('"kind":"blob"') &&
        richProviderItems[6].content === "sdk-rich-after",
      "rich provider text projection changed",
    );
    const richProviderWire = JSON.stringify(richModelRequest);
    check(
      !richProviderWire.includes(
        "file:///sdk/source-image-must-not-forward.png",
      ) && !richProviderWire.includes("must-not-forward"),
      "rich provider request leaked source image URI or reserved metadata",
    );
    const richTranscript = await readFile(
      join(
        workDir,
        projectStateDir,
        "transcripts",
        `${created.sessionId}.jsonl`,
      ),
      "utf8",
    );
    check(
      richTranscript.includes('"version":2') &&
        richTranscript.includes('"kind":"resource_link"') &&
        richTranscript.includes('"kind":"embedded_text"') &&
        richTranscript.includes('"kind":"embedded_blob"'),
      "official SDK rich prompt did not persist the version-2 typed record",
    );
    check(
      !richTranscript.includes(richPNG) &&
        !richTranscript.includes(
          "file:///sdk/source-image-must-not-forward.png",
        ) &&
        !richTranscript.includes("must-not-forward"),
      "official SDK rich transcript leaked bytes, source URI, or _meta",
    );

    trace("echo prompt");
    const firstPrompt = await ctx.request(acp.methods.agent.session.prompt, {
      sessionId: created.sessionId,
      prompt: [{ type: "text", text: "invoke echo" }],
    });
    check(firstPrompt.stopReason === "end_turn", "echo prompt did not finish");
    await waitFor("MCP echo invocation", async () => {
      const calls = await readJSONLines(callRecord);
      return calls.some(
        (call) => call.name === "echo" && call.value === "sdk-v1",
      );
    });
    check(
      updates.some(
        (notification) =>
          notification.sessionId === created.sessionId &&
          notification.update?.sessionUpdate === "tool_call",
      ),
      "official SDK observed no MCP tool_call update",
    );

    trace("shutdown prompt");
    const shutdownPrompt = await ctx.request(
      acp.methods.agent.session.prompt,
      {
        sessionId: created.sessionId,
        prompt: [{ type: "text", text: "shutdown the MCP server" }],
      },
    );
    check(
      shutdownPrompt.stopReason === "end_turn",
      "shutdown prompt did not finish",
    );
    await waitFor(
      "unexpected MCP shutdown",
      async () => !processAlive(firstLaunch.pid),
    );
    trace("active reconnect");
    const secondLaunch = await waitFor("exact active reconnect", async () => {
      await ctx.request(acp.methods.agent.session.resume, {
        sessionId: created.sessionId,
        cwd: workDir,
        mcpServers: [descriptor],
      });
      const launches = await readJSONLines(launchRecord);
      return launches.find((launch) => launch.pid !== firstLaunch.pid);
    });
    trace("rich resume prompt");
    const resumedRichPrompt = await ctx.request(
      acp.methods.agent.session.prompt,
      {
        sessionId: created.sessionId,
        prompt: [
          { type: "text", text: "sdk-rich-resume" },
          { type: "image", data: richPNG, mimeType: "image/png" },
        ],
      },
    );
    check(
      resumedRichPrompt.stopReason === "end_turn",
      "rich resume prompt did not finish",
    );
    trace("close after reconnect");
    await ctx.request(acp.methods.agent.session.close, {
      sessionId: created.sessionId,
    });
    await waitFor(
      "reconnected MCP process cleanup",
      async () => !processAlive(secondLaunch.pid),
    );

    trace("rich load seed session/new");
    const richLoadSeed = await ctx.request(acp.methods.agent.session.new, {
      cwd: workDir,
      mcpServers: [],
    });
    let richLoadSeedFailure;
    let richLoadSeedResponse;
    try {
      richLoadSeedResponse = await ctx.request(
        acp.methods.agent.session.prompt,
        {
          sessionId: richLoadSeed.sessionId,
          prompt: mixedRichPromptBlocks(
            "sdk-rich-load-seed-before",
            "sdk-rich-load-seed-after",
          ),
        },
      );
    } catch (error) {
      richLoadSeedFailure = error;
    }
    check(
      richLoadSeedFailure instanceof acp.RequestError ||
        richLoadSeedResponse?.stopReason === "end_turn",
      "rich load seed did not settle after the intentional provider failure",
    );
    const richLoadSeedTranscript = await readFile(
      join(
        workDir,
        projectStateDir,
        "transcripts",
        `${richLoadSeed.sessionId}.jsonl`,
      ),
      "utf8",
    );
    check(
      richLoadSeedTranscript.includes("sdk-rich-load-seed-before") &&
        richLoadSeedTranscript.includes('"kind":"embedded_blob"') &&
        richLoadSeedTranscript.includes('"kind":"assistant"') &&
        !richLoadSeedTranscript.includes("assistant_output_multi_content"),
      "rich load seed did not retain rich user plus plain terminal error",
    );
    await ctx.request(acp.methods.agent.session.close, {
      sessionId: richLoadSeed.sessionId,
    });

    const beforeRichLoad = updates.length;
    trace("rich seed session/load");
    await ctx.request(acp.methods.agent.session.load, {
      sessionId: richLoadSeed.sessionId,
      cwd: workDir,
      mcpServers: [],
    });
    const richLoadUpdates = updates.slice(beforeRichLoad);
    const richLoadUsers = richLoadUpdates
      .filter(
        (notification) =>
          notification.update?.sessionUpdate === "user_message_chunk",
      )
      .map((notification) => notification.update);
    const richReplayStart = richLoadUsers.findIndex(
      (update) =>
        update.content?.type === "text" &&
        update.content.text === "sdk-rich-load-seed-before",
    );
    const richReplayParts = richLoadUsers.slice(
      richReplayStart,
      richReplayStart + 6,
    );
    check(
      richReplayStart >= 0 && richReplayParts.length === 6,
      "rich load did not replay the complete mixed prompt before response",
    );
    check(
      richReplayParts.every(
        (update) =>
          typeof update.messageId === "string" &&
          update.messageId === richReplayParts[0].messageId,
      ),
      "rich load split one logical prompt across message IDs",
    );
    check(
      JSON.stringify(richReplayParts.map((update) => update.content.type)) ===
        JSON.stringify([
          "text",
          "resource_link",
          "image",
          "resource",
          "resource",
          "text",
        ]),
      "rich load changed logical ACP block order",
    );
    const replayResourceLink = richReplayParts[1].content;
    const replayImage = richReplayParts[2].content;
    const replayEmbeddedText = richReplayParts[3].content;
    const replayEmbeddedBlob = richReplayParts[4].content;
    check(
      replayResourceLink.uri === "file:///sdk/schema.json" &&
        replayResourceLink.name === "schema.json" &&
        replayResourceLink.mimeType === "application/json" &&
        JSON.stringify(replayResourceLink.annotations?.audience) ===
          JSON.stringify(["assistant"]) &&
        replayResourceLink.annotations?.priority === 0.5,
      "rich load changed resource-link metadata or annotations",
    );
    check(
      replayImage.data === richPNG &&
        replayImage.mimeType === "image/png" &&
        replayImage.uri === undefined &&
        JSON.stringify(replayImage.annotations?.audience) ===
          JSON.stringify(["user"]),
      "rich load changed image bytes, MIME, annotations, or source-URI policy",
    );
    check(
      replayEmbeddedText.resource?.uri === "file:///sdk/context.txt" &&
        replayEmbeddedText.resource?.mimeType === "text/plain" &&
        replayEmbeddedText.resource?.text === "sdk embedded text" &&
        replayEmbeddedText.resource?.blob === undefined &&
        JSON.stringify(replayEmbeddedText.annotations?.audience) ===
          JSON.stringify(["assistant"]),
      "rich load changed embedded-text identity or annotations",
    );
    check(
      replayEmbeddedBlob.type === "resource" &&
        replayEmbeddedBlob.resource?.uri === "file:///sdk/pixel.png" &&
        replayEmbeddedBlob.resource?.mimeType === "image/png" &&
        replayEmbeddedBlob.resource?.blob === richPNG &&
        replayEmbeddedBlob.resource?.text === undefined,
      "rich load did not preserve embedded blob as one logical resource",
    );
    const richReplayWire = JSON.stringify(richReplayParts);
    check(
      !richReplayWire.includes("_meta") &&
        !richReplayWire.includes("must-not-forward") &&
        !richReplayWire.includes(
          "file:///sdk/source-image-must-not-forward.png",
        ),
      "rich load leaked reserved metadata or source image URI",
    );
    trace("close after rich load");
    await ctx.request(acp.methods.agent.session.close, {
      sessionId: richLoadSeed.sessionId,
    });

    trace("provider-rich session/new and provider seed");
    const providerRich = await ctx.request(acp.methods.agent.session.new, {
      cwd: workDir,
      mcpServers: [],
    });
    const providerRichSeed = await ctx.request(
      acp.methods.agent.session.prompt,
      {
        sessionId: providerRich.sessionId,
        prompt: [{ type: "text", text: "sdk-provider-rich-seed" }],
      },
    );
    check(
      providerRichSeed.stopReason === "end_turn",
      "provider-rich seed did not finish",
    );
    await ctx.request(acp.methods.agent.session.close, {
      sessionId: providerRich.sessionId,
    });
    const providerRichTranscript = join(
      workDir,
      projectStateDir,
      "transcripts",
      `${providerRich.sessionId}.jsonl`,
    );
    const providerRichTranscriptText = await readFile(
      providerRichTranscript,
      "utf8",
    );
    check(
      providerRichTranscriptText.includes(
        '"assistant_output_multi_content"',
      ) &&
        providerRichTranscriptText.includes("sdk-private-reasoning-text") &&
        providerRichTranscriptText.includes("sdk-private-signature-value"),
      "provider-rich seed did not persist Agentic continuation output",
    );

    const beforeProviderRichLoad = updates.length;
    trace("provider-rich session/load");
    await ctx.request(acp.methods.agent.session.load, {
      sessionId: providerRich.sessionId,
      cwd: workDir,
      mcpServers: [],
    });
    const providerRichReplay = updates.slice(beforeProviderRichLoad);
    const providerRichChunks = providerRichReplay
      .filter(
        (notification) =>
          notification.update?.sessionUpdate === "agent_message_chunk",
      )
      .map((notification) => notification.update);
    check(
      JSON.stringify(providerRichChunks.map((update) => update.content?.text)) ===
        JSON.stringify([
          "",
          "sdk-provider-rich-one ",
          "",
          "",
          "sdk-provider-rich-two",
          "",
        ]),
      "provider-rich load did not replay the exact ordered public text",
    );
    check(
      providerRichReplay.some(
        (notification) =>
          notification.update?.sessionUpdate === "user_message_chunk" &&
          notification.update.content?.text === "sdk-provider-rich-seed",
      ),
      "provider-rich load lost the durable user replay",
    );
    check(
      providerRichChunks.length === 6 &&
        providerRichChunks.every(
          (update) => update.messageId === undefined,
        ),
      "provider-rich rollback mode exposed an assistant message ID",
    );
    const providerRichWire = JSON.stringify(providerRichReplay);
    check(
      !providerRichWire.includes("sdk-private-reasoning-text") &&
        !providerRichWire.includes("sdk-private-signature-value") &&
        !providerRichWire.includes("reasoning") &&
        !providerRichWire.includes("agent_thought_chunk"),
      "provider-rich load leaked private reasoning or signature",
    );
    trace("provider-rich continuation after load");
    const providerRichContinuationStart = modelRequests.length;
    const providerRichContinuationUpdateStart = updates.length;
    const providerRichContinuation = await ctx.request(
      acp.methods.agent.session.prompt,
      {
        sessionId: providerRich.sessionId,
        prompt: [
          { type: "text", text: "sdk-provider-rich-followup-question" },
        ],
      },
    );
    const providerRichContinuationRequests = modelRequests
      .slice(providerRichContinuationStart)
      .filter((request) =>
        JSON.stringify(request.input).includes(
          "sdk-provider-rich-followup-question",
        ),
      );
    trace(
      `provider-rich continuation result ${JSON.stringify({
        response: providerRichContinuation,
        modelRequestCount:
          modelRequests.length - providerRichContinuationStart,
        matchingModelRequestCount: providerRichContinuationRequests.length,
        updates: updates.slice(providerRichContinuationUpdateStart),
      })}`,
    );
    check(
      providerRichContinuation.stopReason === "end_turn" &&
        providerRichContinuationRequests.length === 1 &&
        updates
          .slice(providerRichContinuationUpdateStart)
          .some(
            (update) =>
              update.update?.sessionUpdate === "agent_message_chunk" &&
              update.update?.content?.text ===
                "sdk-provider-rich-continuation-ok",
          ),
      "provider-rich load did not preserve a working continuation",
    );
    trace("close after provider-rich load");
    await ctx.request(acp.methods.agent.session.close, {
      sessionId: providerRich.sessionId,
    });

    trace("lifecycle session/new");
    const lifecycle = await ctx.request(acp.methods.agent.session.new, {
      cwd: workDir,
      mcpServers: [descriptor],
    });
    const thirdLaunch = await waitFor("lifecycle new MCP launch", async () => {
      const launches = await readJSONLines(launchRecord);
      return launches.find(
        (launch) =>
          ![firstLaunch.pid, secondLaunch.pid].includes(launch.pid),
      );
    });
    await ctx.request(acp.methods.agent.session.close, {
      sessionId: lifecycle.sessionId,
    });
    await waitFor(
      "lifecycle new MCP cleanup",
      async () => !processAlive(thirdLaunch.pid),
    );
    const lifecycleTranscript = join(
      workDir,
      projectStateDir,
      "transcripts",
      `${lifecycle.sessionId}.jsonl`,
    );
    await appendFile(
      lifecycleTranscript,
      `${JSON.stringify({
        timestamp: new Date().toISOString(),
        entry_id: {
          version: 1,
          id: "11111111111111111111111111111111",
        },
        kind: "user",
        message: { role: "user", content: "official SDK replay fixture" },
      })}\n`,
    );

    const beforeLoad = updates.length;
    trace("session/load");
    await ctx.request(acp.methods.agent.session.load, {
      sessionId: lifecycle.sessionId,
      cwd: workDir,
      mcpServers: [descriptor],
    });
    const fourthLaunch = await waitFor("load MCP launch", async () => {
      const launches = await readJSONLines(launchRecord);
      return launches.find(
        (launch) =>
          ![firstLaunch.pid, secondLaunch.pid, thirdLaunch.pid].includes(
            launch.pid,
          ),
      );
    });
    check(
      updates.slice(beforeLoad).some(
        (notification) =>
          notification.update?.sessionUpdate === "user_message_chunk",
      ),
      "load emitted no durable user replay through the official SDK",
    );
    trace("close after load");
    await ctx.request(acp.methods.agent.session.close, {
      sessionId: lifecycle.sessionId,
    });
    await waitFor(
      "load MCP process cleanup",
      async () => !processAlive(fourthLaunch.pid),
    );

    const beforeResume = updates.length;
    trace("session/resume");
    await ctx.request(acp.methods.agent.session.resume, {
      sessionId: lifecycle.sessionId,
      cwd: workDir,
      mcpServers: [descriptor],
    });
    const fifthLaunch = await waitFor("resume MCP launch", async () => {
      const launches = await readJSONLines(launchRecord);
      return launches.find(
        (launch) =>
          ![
            firstLaunch.pid,
            secondLaunch.pid,
            thirdLaunch.pid,
            fourthLaunch.pid,
          ].includes(launch.pid),
      );
    });
    check(
      !updates.slice(beforeResume).some((notification) => {
        const kind = notification.update?.sessionUpdate;
        return kind === "user_message_chunk" || kind === "agent_message_chunk";
      }),
      "resume replayed conversation",
    );
    const afterResumeSetup = updates.length;

    let failure;
    trace("failure probe");
    try {
      await ctx.request(acp.methods.agent.session.new, {
        cwd: workDir,
        mcpServers: [
          {
            name: "missing-server",
            command: join(workDir, "missing-command"),
            args: [],
            env: [],
          },
        ],
      });
    } catch (error) {
      failure = error;
    }
    check(failure instanceof acp.RequestError, "setup failure was not typed");
    check(
      !JSON.stringify(failure).includes("missing-command"),
      "setup failure leaked the command path",
    );

    trace("final close");
    await ctx.request(acp.methods.agent.session.close, {
      sessionId: lifecycle.sessionId,
    });
    await waitFor(
      "resume MCP process cleanup",
      async () => !processAlive(fifthLaunch.pid),
    );

    return {
      sdk: `${sdkPackage.name}@${sdkPackage.version}`,
      negotiatedProtocolVersion: initialized.protocolVersion,
      sessionId: created.sessionId,
      launches: (await readJSONLines(launchRecord)).length,
      calls: await readJSONLines(callRecord),
      modelRequests: modelRequests.length,
      richProviderParts: richTypes,
      richPromptCapabilities:
        initialized.agentCapabilities.promptCapabilities,
      richReplayKinds: richReplayParts.map(
        (update) => update.content.type,
      ),
      replayUpdates: updates.slice(beforeLoad, beforeResume).length,
      resumeConversationUpdates: updates
        .slice(beforeResume, afterResumeSetup)
        .filter((notification) => {
          const kind = notification.update?.sessionUpdate;
          return kind === "user_message_chunk" || kind === "agent_message_chunk";
        }).length,
      failureCode: failure.code,
    };
  });
} finally {
  agent.kill("SIGTERM");
  modelServer.close();
}

check(
  !agentStderr.join("").includes("p23-private-sdk-value"),
  "agent stderr leaked descriptor environment",
);
console.log(JSON.stringify(result, null, 2));

if (process.env.P23_KEEP_WORKDIR === "1") {
  process.stderr.write(`[p23-harness] kept workdir ${workDir}\n`);
} else {
  await rm(workDir, { recursive: true, force: true });
}
