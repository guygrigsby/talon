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

/**
 * State held by the running shim: the resolved extension definition,
 * captured registrations, and the realized tools map. The map is
 * populated lazily on first RunTool (and on any call that needs tool
 * metadata) so that Initialize doesn't have to construct execution
 * contexts before the host's first dispatch.
 */
class State {
  /** @param {{ id: string, name: string, description: string, captured: import("./api-shim.mjs").Captured }} loaded */
  constructor(loaded) {
    this.loaded = loaded;
    /** @type {Map<string, any> | null} */
    this.toolsRealized = null;
  }

  /** Build a tool execution context. PHASE 1: minimal — extensions that
   *  rely on rich ctx (logger, services, runtime config) will see undefined
   *  fields. We document this on talon-o0h so the next phase fills them in.
   */
  buildToolContext() {
    return {
      // Stub: openclaw tool factories may read ctx.config, ctx.logger,
      // etc. We provide a logger and let the rest be undefined; tools
      // that throw on missing fields will surface a clear error.
      logger: console,
    };
  }

  ensureToolsRealized() {
    if (this.toolsRealized != null) return this.toolsRealized;
    const ctx = this.buildToolContext();
    this.toolsRealized = this.loaded.captured.realizeTools(ctx);
    return this.toolsRealized;
  }

  /** Build the Initialize manifest from captured registrations. */
  buildManifest() {
    const tools = this.ensureToolsRealized();
    const offersTools = [];
    for (const [name, tool] of tools) {
      offersTools.push({
        name,
        description: tool?.description ?? tool?.label ?? "",
        parameters_schema: toJsonSchemaBytes(tool?.parameters),
      });
    }
    return {
      name: this.loaded.id || this.loaded.name || "openclaw-shim",
      version: "0.1.0",
      description: this.loaded.description || "openclaw extension via talon compat shim",
      offers_tools: offersTools,
      offers_providers: [],
      offers_channels: [],
      // PHASE 1: don't request any host-side capabilities. Phase 2+
      // will infer needs from which register* methods the extension
      // actually called (e.g. registerWebSearchProvider → needs
      // network policy if/when we add one).
      needs: [],
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
      try {
        const manifest = state.buildManifest();
        callback(null, { manifest });
      } catch (err) {
        callback({
          code: grpc.status.INTERNAL,
          message: `openclaw-shim Initialize: ${err?.stack ?? err}`,
        });
      }
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
      const tools = state.ensureToolsRealized();
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

    // PHASE 1 stubs for unsupported RPCs. The interceptor in talon
    // can't tell the difference between "method not implemented" and
    // "method explicitly disabled" — return UNIMPLEMENTED so the
    // chat-side layer (which calls these only when the manifest says
    // they're offered) treats it as a programming error worth fixing.
    StreamCompletion: (call) => {
      call.emit("error", {
        code: grpc.status.UNIMPLEMENTED,
        message: "openclaw-shim: StreamCompletion not bridged in Phase 1",
      });
    },
    StartChannel: (call) => {
      call.emit("error", {
        code: grpc.status.UNIMPLEMENTED,
        message: "openclaw-shim: StartChannel not bridged in Phase 1",
      });
    },
    SendChannelMessage: (_call, callback) => {
      callback({
        code: grpc.status.UNIMPLEMENTED,
        message: "openclaw-shim: SendChannelMessage not bridged in Phase 1",
      });
    },
  };
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
  const state = new State(loaded);
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
