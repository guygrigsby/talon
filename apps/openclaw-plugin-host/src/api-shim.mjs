// api-shim provides a partial OpenClawPluginApi that an extension's
// register(api) method can call into. We capture every registration so
// the gRPC Initialize handler can build a Manifest from it, and
// RunTool/StartChannel/etc can dispatch to the captured handlers.
//
// PHASE 1 (MVP): only registerTool is fully wired. The rest of the API
// surface (registerHttpRoute, registerWebSearchProvider,
// registerCommand, ...) is captured-but-ignored: we record that the
// extension would have used it so users get a clear "shim doesn't yet
// support X" log line, but the lack of support doesn't crash the load.
//
// Adding a new register-method bridge later is mechanical: capture the
// call here, expose it on Captured, and translate in grpc-server.mjs.

/**
 * Captured is the bag the loader fills in via api.register*() calls.
 * Read by grpc-server when assembling Initialize responses.
 */
export class Captured {
  constructor() {
    /** @type {Array<{ name: string, factory: Function, options: object }>} */
    this.toolFactories = [];
    /** @type {Array<{ kind: string, args: any[] }>} */
    this.unsupported = [];
  }

  /**
   * Realize the registered tool factories against a context. Returns a
   * map keyed by tool name. Called once at Initialize time, before the
   * first RunTool. Re-realizing on every RunTool would be cleaner for
   * config hot-reload but openclaw extensions historically expect the
   * factory to run once per process lifetime.
   */
  realizeTools(ctx) {
    /** @type {Map<string, any>} */
    const tools = new Map();
    for (const { name: hint, factory, options } of this.toolFactories) {
      let tool;
      try {
        tool = factory(ctx);
      } catch (err) {
        process.stderr.write(
          `[openclaw-shim] tool factory for ${JSON.stringify(hint || options.name || "?")} threw: ${err?.stack ?? err}\n`,
        );
        continue;
      }
      const name = tool?.name ?? options?.name ?? hint;
      if (!name) {
        process.stderr.write(
          `[openclaw-shim] tool factory returned an object without a name; dropping\n`,
        );
        continue;
      }
      tools.set(name, tool);
    }
    return tools;
  }
}

/**
 * Build a partial OpenClawPluginApi for the extension's register(api)
 * call. Every method that mutates registry state writes into captured;
 * methods we don't yet bridge log a one-line warning so the user sees
 * what's missing.
 *
 * @param {Captured} captured
 */
export function buildApi(captured) {
  const warn = (kind, args) => {
    captured.unsupported.push({ kind, args });
    process.stderr.write(
      `[openclaw-shim] api.${kind}(): not yet bridged by the compat shim — ignoring\n`,
    );
  };

  return {
    // === bridged ====================================================
    registerTool(factory, options = {}) {
      // openclaw signature: registerTool(factory, { name?: string }).
      // Some extensions pass a tool object directly instead of a
      // factory; accept both for forward compat.
      if (typeof factory === "function") {
        captured.toolFactories.push({
          name: options?.name ?? "",
          factory,
          options: options ?? {},
        });
      } else if (factory && typeof factory === "object") {
        captured.toolFactories.push({
          name: factory.name ?? options?.name ?? "",
          factory: () => factory,
          options: options ?? {},
        });
      } else {
        process.stderr.write(
          `[openclaw-shim] registerTool: ignored, expected function or object, got ${typeof factory}\n`,
        );
      }
    },

    // === captured-but-ignored (Phase 2+) ============================
    registerHttpRoute: (...args) => warn("registerHttpRoute", args),
    registerWebSearchProvider: (...args) => warn("registerWebSearchProvider", args),
    registerProvider: (...args) => warn("registerProvider", args),
    registerCommand: (...args) => warn("registerCommand", args),
    registerService: (...args) => warn("registerService", args),
    registerMemory: (...args) => warn("registerMemory", args),
    registerContextEngine: (...args) => warn("registerContextEngine", args),
    registerAgentHarness: (...args) => warn("registerAgentHarness", args),
    registerHook: (...args) => warn("registerHook", args),

    // Logger surface — extensions call api.logger.info/warn/error. Map
    // to stderr so the host captures it as `[plugin/<name>] <line>`.
    logger: {
      info: (...args) => process.stderr.write(`[shim:info] ${formatArgs(args)}\n`),
      warn: (...args) => process.stderr.write(`[shim:warn] ${formatArgs(args)}\n`),
      error: (...args) => process.stderr.write(`[shim:error] ${formatArgs(args)}\n`),
      debug: (...args) => process.stderr.write(`[shim:debug] ${formatArgs(args)}\n`),
    },
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
