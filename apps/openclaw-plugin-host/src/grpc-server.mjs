import path from "node:path";
import { fileURLToPath } from "node:url";

import grpc from "@grpc/grpc-js";
import protoLoader from "@grpc/proto-loader";

import { toJsonSchemaBytes } from "./json-schema.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
// proto/ lives next to src/ at the package root.
const PROTO_PATH = path.resolve(__dirname, "..", "proto", "plugin.proto");

const PROTO_DEF = protoLoader.loadSync(PROTO_PATH, {
  keepCase: true,
  longs: String,
  enums: String,
  defaults: true,
  arrays: true,
  oneofs: true,
});
const PROTO = grpc.loadPackageDefinition(PROTO_DEF).talon.plugin.v1;
const PROTO_HOST = PROTO; // Host service lives in the same package.

/**
 * State held by the running shim: the resolved extension definition,
 * captured registrations, and the realized tools map. The map is
 * populated lazily on the first call that needs it so Initialize can
 * await the host's GetConfig before building tool factories.
 */
class State {
  /** @param {{ id: string, name: string, description: string, captured: import("./api-shim.mjs").Captured }} loaded */
  /** @param {{ authCookie: string, hostAddress: string }} hostHandshake */
  constructor(loaded, hostHandshake) {
    this.loaded = loaded;
    this.hostHandshake = hostHandshake;
    /** @type {Map<string, any> | null} */
    this.toolsRealized = null;
    /** @type {object | null} cached parsed config from host GetConfig */
    this.cachedConfig = null;
  }

  /**
   * Fetch the merged config from the host once and cache it. Returns
   * null when the host service isn't reachable or returns invalid JSON
   * — extensions that require ctx.config will see null and can decide
   * how to react. Failing here would block extensions that don't read
   * config at all.
   */
  async fetchHostConfig() {
    if (this.cachedConfig !== null) return this.cachedConfig;
    if (!this.hostHandshake.hostAddress || !this.hostHandshake.authCookie) {
      return null;
    }
    try {
      const client = new PROTO_HOST.Host(
        this.hostHandshake.hostAddress,
        grpc.credentials.createInsecure(),
      );
      // Metadata key must match plugin/handshake.go's CookieMetadataKey
      // ("talon-plugin-auth-cookie"); the host's interceptor rejects
      // calls without it as Unauthenticated.
      const meta = new grpc.Metadata();
      meta.set("talon-plugin-auth-cookie", this.hostHandshake.authCookie);
      const raw = await new Promise((resolve, reject) => {
        client.GetConfig({ path: "" }, meta, (err, resp) => {
          if (err) {
            reject(err);
            return;
          }
          resolve(resp?.raw_json ?? null);
        });
      });
      if (!raw) return null;
      const text = Buffer.isBuffer(raw) ? raw.toString("utf-8") : String(raw);
      this.cachedConfig = JSON.parse(text);
      return this.cachedConfig;
    } catch (err) {
      process.stderr.write(
        `[openclaw-shim] host GetConfig failed: ${err?.message ?? err}\n`,
      );
      return null;
    }
  }

  /**
   * Build a tool / provider / channel execution context. The shape
   * mirrors openclaw extensions' expectations — config (the merged
   * gateway config), logger, and a small services bag for surfaces
   * we don't yet bridge but extensions check for existence.
   */
  async buildContext() {
    const config = await this.fetchHostConfig();
    return {
      config,
      // Logger: openclaw extensions call ctx.logger.{info,warn,error,debug}.
      // Map to stderr lines that the host captures with the plugin
      // name prefixed, same shape as the api logger.
      logger: {
        info: (...args) => process.stderr.write(`[shim:info] ${formatArgs(args)}\n`),
        warn: (...args) => process.stderr.write(`[shim:warn] ${formatArgs(args)}\n`),
        error: (...args) => process.stderr.write(`[shim:error] ${formatArgs(args)}\n`),
        debug: (...args) => process.stderr.write(`[shim:debug] ${formatArgs(args)}\n`),
      },
      // Services bag: deliberately empty for Phase 2. openclaw extensions
      // historically check ctx.services?.<name> with optional chaining
      // so a stub object is more robust than undefined.
      services: {},
    };
  }

  async realizeAll() {
    if (this.toolsRealized != null) return;
    const ctx = await this.buildContext();
    this.toolsRealized = this.loaded.captured.realizeTools(ctx);
    this.providersRealized = this.loaded.captured.realizeProviders(ctx);
    this.channelsRealized = this.loaded.captured.realizeChannels(ctx);
  }

  async ensureToolsRealized() {
    await this.realizeAll();
    return this.toolsRealized;
  }
  async ensureProvidersRealized() {
    await this.realizeAll();
    return this.providersRealized;
  }
  async ensureChannelsRealized() {
    await this.realizeAll();
    return this.channelsRealized;
  }

  /** Build the Initialize manifest from captured registrations. */
  async buildManifest() {
    await this.realizeAll();

    const offersTools = [];
    for (const [name, tool] of this.toolsRealized) {
      offersTools.push({
        name,
        description: tool?.description ?? tool?.label ?? "",
        parameters_schema: toJsonSchemaBytes(tool?.parameters),
      });
    }
    const offersProviders = [];
    for (const [name, prov] of this.providersRealized) {
      // openclaw providers expose a `models` array — either strings
      // (model ids) or objects with .id. Normalize to strings; the
      // host's manifest schema is `repeated string models`.
      const models = (prov?.models ?? []).map((m) =>
        typeof m === "string" ? m : (m?.id ?? ""),
      ).filter(Boolean);
      offersProviders.push({ name, models });
    }
    const offersChannels = [];
    for (const [name] of this.channelsRealized) {
      offersChannels.push(name);
    }
    return {
      name: this.loaded.id || this.loaded.name || "openclaw-shim",
      version: "0.1.0",
      description: this.loaded.description || "openclaw extension via talon compat shim",
      offers_tools: offersTools,
      offers_providers: offersProviders,
      offers_channels: offersChannels,
      // Request the capabilities we actually call back into the host
      // for. Today: GetConfig (CAPABILITY_READ_CONFIG). The host's
      // interceptor will reject calls to RPCs not covered by these.
      needs: ["CAPABILITY_READ_CONFIG"],
    };
  }
}

/**
 * Build the gRPC service handlers. The handlers close over `state` so
 * Initialize/Shutdown/RunTool see the same captured registrations.
 *
 * @param {State} state
 * @param {{ shutdown: () => void }} lifecycle
 */
function buildHandlers(state, lifecycle) {
  return {
    Initialize: (call, callback) => {
      // The host passes auth_cookie + host_address in the request so
      // the shim can dial Host service methods (GetConfig, etc).
      // Store them on state before manifest assembly — manifest
      // build awaits GetConfig.
      const req = call.request ?? {};
      if (req.auth_cookie) state.hostHandshake.authCookie = req.auth_cookie;
      if (req.host_address) state.hostHandshake.hostAddress = req.host_address;
      state
        .buildManifest()
        .then((manifest) => callback(null, { manifest }))
        .catch((err) =>
          callback({
            code: grpc.status.INTERNAL,
            message: `openclaw-shim Initialize: ${err?.stack ?? err}`,
          }),
        );
    },

    Shutdown: (_call, callback) => {
      callback(null, {});
      // Defer the actual exit so the gRPC reply ships before the
      // server stops. The host will follow up with a SIGKILL if the
      // process lingers.
      setImmediate(() => lifecycle.shutdown());
    },

    RunTool: async (call, callback) => {
      const req = call.request;
      const name = req.tool_name;
      const tools = await state.ensureToolsRealized();
      const tool = tools.get(name);
      if (!tool) {
        callback(null, {
          output: `openclaw-shim: unknown tool ${JSON.stringify(name)}`,
          is_error: true,
        });
        return;
      }
      let parsed;
      try {
        parsed = req.arguments_json ? JSON.parse(req.arguments_json) : {};
      } catch (err) {
        callback(null, {
          output: `openclaw-shim: invalid arguments JSON for ${name}: ${err?.message ?? err}`,
          is_error: true,
        });
        return;
      }
      try {
        // openclaw tool execute signature: execute(toolCallId, params).
        const toolCallId = req.run_id ?? "";
        const result = await tool.execute(toolCallId, parsed);
        callback(null, {
          output: stringifyResult(result),
          is_error: false,
        });
      } catch (err) {
        callback(null, {
          output: `openclaw-shim: tool ${name} threw: ${err?.message ?? err}`,
          is_error: true,
        });
      }
    },

    StreamCompletion: async (call) => {
      // Provider chat-completion bridge. openclaw providers expose
      // streamChatCompletion(req) → AsyncIterable<chunk> where each
      // chunk has { kind: "text"|"tool_call"|"usage"|"error", ... }.
      // We translate each chunk to a talon Delta and write it to the
      // gRPC stream. Errors abort the call with INTERNAL.
      try {
        const req = call.request;
        const providers = await state.ensureProvidersRealized();
        const provider = pickProvider(providers, req.model);
        if (!provider) {
          call.emit("error", {
            code: grpc.status.NOT_FOUND,
            message: `openclaw-shim: no registered provider serves model ${JSON.stringify(req.model)}`,
          });
          return;
        }
        const openclawReq = adaptCompletionRequest(req);
        const stream = await provider.streamChatCompletion(openclawReq);
        for await (const chunk of toAsyncIterable(stream)) {
          const delta = adaptCompletionChunk(chunk);
          if (delta) call.write(delta);
        }
        call.end();
      } catch (err) {
        call.emit("error", {
          code: grpc.status.INTERNAL,
          message: `openclaw-shim StreamCompletion: ${err?.stack ?? err?.message ?? err}`,
        });
      }
    },

    StartChannel: async (call) => {
      // Channel inbound bridge. openclaw channels expose
      // start({ onMessage }) where the channel calls onMessage(msg)
      // for each incoming message. We forward each msg to the gRPC
      // stream as IncomingChannelMessage. Cancellation: when the
      // gRPC client closes the stream, call.cancelled fires; we
      // invoke the channel's stop() if it has one.
      try {
        const req = call.request;
        const channels = await state.ensureChannelsRealized();
        const channel = channels.get(req.channel_name);
        if (!channel) {
          call.emit("error", {
            code: grpc.status.NOT_FOUND,
            message: `openclaw-shim: unknown channel ${JSON.stringify(req.channel_name)}`,
          });
          return;
        }
        const onMessage = (msg) => {
          call.write(adaptIncomingMessage(req.channel_name, msg));
        };
        const ret = channel.start({ onMessage });
        // start() may return a stop fn or a Promise<stopFn>; tolerate
        // both. We capture it so the cancel handler can call it.
        const stop = await Promise.resolve(ret);
        call.on("cancelled", () => {
          try {
            if (typeof stop === "function") stop();
            else if (typeof channel.stop === "function") channel.stop();
          } catch (err) {
            process.stderr.write(
              `[openclaw-shim] channel ${req.channel_name} stop: ${err?.message ?? err}\n`,
            );
          }
        });
      } catch (err) {
        call.emit("error", {
          code: grpc.status.INTERNAL,
          message: `openclaw-shim StartChannel: ${err?.stack ?? err?.message ?? err}`,
        });
      }
    },

    SendChannelMessage: async (call, callback) => {
      // SendChannelMessageResponse is just { ok: bool } — no error
      // string in the proto. Failures come back as ok=false; the host
      // logs whatever stderr we wrote so the user sees the cause.
      const req = call.request;
      // Trace each call so e2e tests can assert on outbound text the
      // same way testplugin/main.go's stderr line is asserted on.
      process.stderr.write(
        `openclaw-shim: SendChannelMessage channel=${JSON.stringify(req.channel)} room=${JSON.stringify(req.room_id)} text=${JSON.stringify(req.text)}\n`,
      );
      try {
        const channels = await state.ensureChannelsRealized();
        const channel = channels.get(req.channel);
        if (!channel || typeof channel.send !== "function") {
          process.stderr.write(
            `[openclaw-shim] SendChannelMessage: channel ${JSON.stringify(req.channel)} has no send()\n`,
          );
          callback(null, { ok: false });
          return;
        }
        await channel.send({
          roomId: req.room_id,
          text: req.text,
        });
        callback(null, { ok: true });
      } catch (err) {
        process.stderr.write(
          `[openclaw-shim] SendChannelMessage threw: ${err?.stack ?? err?.message ?? err}\n`,
        );
        callback(null, { ok: false });
      }
    },
  };
}

// pickProvider matches a "provider/model" id to a captured provider.
// openclaw provider names typically equal the leading segment (e.g.
// "openai/gpt-4o" → "openai"); but plugins can register multiple
// providers per shim, so we fall back to scanning each provider's
// .models list for an exact match.
function pickProvider(providers, modelID) {
  if (!modelID) return null;
  const slash = modelID.indexOf("/");
  if (slash > 0) {
    const name = modelID.slice(0, slash);
    const direct = providers.get(name);
    if (direct) return direct;
  }
  for (const [, prov] of providers) {
    const models = prov?.models ?? [];
    for (const m of models) {
      const id = typeof m === "string" ? m : m?.id;
      if (id === modelID) return prov;
    }
  }
  return null;
}

// adaptCompletionRequest translates a talon StreamCompletionRequest
// (proto) into the { model, system, messages, tools, ... } shape
// openclaw providers expect. Field names are snake_case on the wire
// (keepCase: true) so we just rename for ergonomic openclaw use.
function adaptCompletionRequest(req) {
  return {
    model: req.model,
    system: req.system ?? "",
    messages: (req.messages ?? []).map((m) => ({
      role: roleEnumToString(m.role),
      content: m.content ?? "",
      toolCalls: m.tool_calls ?? undefined,
      toolCallId: m.tool_call_id ?? undefined,
    })),
    tools: req.tools ?? [],
    temperature: req.temperature,
    maxOutputTokens: req.max_output_tokens,
  };
}

// roleEnumToString converts the proto enum (which proto-loader hands
// us as a string like "ROLE_USER") to the lowercase form openclaw
// uses ("user"). Robust to either form for forward-compat.
function roleEnumToString(role) {
  if (typeof role !== "string") return "user";
  const lower = role.toLowerCase();
  if (lower.startsWith("role_")) return lower.slice("role_".length);
  return lower;
}

// adaptCompletionChunk translates an openclaw stream chunk into a
// talon Delta. openclaw chunk shapes (best-effort, several variants
// in the wild):
//   { kind: "text", text }
//   { kind: "tool_call", toolCall: { id, name, argumentsJson } }
//   { kind: "usage", usage: { inputTokens, outputTokens } }
//   { kind: "error", error: string }
// Plus convenience: a plain { text: "..." } object becomes a text delta.
function adaptCompletionChunk(chunk) {
  if (!chunk) return null;
  if (typeof chunk.text === "string" && !chunk.kind) {
    return { text: chunk.text };
  }
  switch (chunk.kind) {
    case "text":
      return { text: chunk.text ?? "" };
    case "tool_call":
      if (!chunk.toolCall) return null;
      return {
        tool_call: {
          id: chunk.toolCall.id ?? "",
          name: chunk.toolCall.name ?? "",
          arguments_json: chunk.toolCall.argumentsJson ?? chunk.toolCall.arguments_json ?? "",
        },
      };
    case "usage":
      return {
        usage: {
          input_tokens: Number(chunk.usage?.inputTokens ?? chunk.usage?.input_tokens ?? 0),
          output_tokens: Number(chunk.usage?.outputTokens ?? chunk.usage?.output_tokens ?? 0),
        },
      };
    case "error":
      // Delta.error is a plain string in the proto's oneof, not an
      // object — set it directly.
      return { error: String(chunk.error ?? "stream error") };
    default:
      return null;
  }
}

// toAsyncIterable accepts (a) an async iterable, (b) a sync iterable,
// or (c) an EventEmitter-shaped readable stream. We normalize to the
// async-iterable form so the StreamCompletion handler can use one
// `for await` loop regardless of which return shape the openclaw
// provider produced.
async function* toAsyncIterable(stream) {
  if (stream == null) return;
  if (stream[Symbol.asyncIterator]) {
    yield* stream;
    return;
  }
  if (stream[Symbol.iterator]) {
    for (const v of stream) yield v;
    return;
  }
  // Fallback: assume Node Readable in object mode.
  for await (const v of stream) yield v;
}

// adaptIncomingMessage translates an openclaw inbound message to the
// IncomingChannelMessage proto. openclaw fields (camelCase) get
// renamed to the snake_case wire shape; we keepCase: true on the
// proto-loader side so the JS object literal matches the wire format
// directly.
function adaptIncomingMessage(channelName, msg) {
  return {
    channel: channelName,
    sender_id: msg.senderId ?? msg.sender_id ?? "",
    display_name: msg.displayName ?? msg.display_name ?? "",
    room_id: msg.roomId ?? msg.room_id ?? "",
    text: msg.text ?? "",
    ts_ms: Number(msg.tsMs ?? msg.ts_ms ?? Date.now()),
  };
}

function formatArgs(args) {
  return args
    .map((a) => {
      if (typeof a === "string") return a;
      try {
        return JSON.stringify(a);
      } catch {
        return String(a);
      }
    })
    .join(" ");
}

function stringifyResult(result) {
  if (result == null) return "";
  if (typeof result === "string") return result;
  try {
    return JSON.stringify(result);
  } catch {
    return String(result);
  }
}

/**
 * Start a gRPC server on 127.0.0.1:0 and return { server, address }.
 * The caller emits the handshake line with the resolved address.
 */
export async function startServer(loaded) {
  // hostHandshake is filled in from the InitializeRequest the host
  // sends after dialing us; we populate it lazily so startup ordering
  // doesn't matter (the gRPC server has to be up before the host
  // dials, and the host dials before sending Initialize).
  const state = new State(loaded, { authCookie: "", hostAddress: "" });
  const server = new grpc.Server();
  const lifecycle = {
    shutdown: () => {
      server.tryShutdown(() => {});
      // Force exit shortly after — same shape as testplugin/main.go.
      setTimeout(() => process.exit(0), 100).unref();
    },
  };
  server.addService(PROTO.Plugin.service, buildHandlers(state, lifecycle));
  const address = await new Promise((resolve, reject) => {
    server.bindAsync("127.0.0.1:0", grpc.ServerCredentials.createInsecure(), (err, port) => {
      if (err) {
        reject(err);
        return;
      }
      resolve(`127.0.0.1:${port}`);
    });
  });
  return { server, address, state };
}
