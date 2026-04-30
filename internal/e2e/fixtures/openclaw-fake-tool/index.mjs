// Test fixture: a minimal openclaw extension that mimics the
// definePluginEntry({ register }) shape without depending on the
// openclaw runtime. The shim should treat this identically to a
// "real" bundled openclaw extension since the wire shape is the same:
// default export { register, id, name, description }.
//
// Phase 2 additions: also exercises registerProvider and
// registerChannel so the shim's StreamCompletion + StartChannel +
// SendChannelMessage bridges have something to drive in tests.

const fakeTool = (ctx) => ({
  name: "fake-echo",
  label: "Fake Echo",
  description: "Echoes the 'text' parameter back. Test fixture.",
  parameters: {
    type: "object",
    properties: {
      text: { type: "string", description: "The text to echo" },
    },
    required: ["text"],
    additionalProperties: false,
  },
  execute: async (_toolCallId, params) => {
    // Tag the response with whether ctx.config was reachable so the
    // Phase-2 e2e test can assert the host GetConfig path landed.
    const cfgVisible = ctx?.config ? "yes" : "no";
    return `echo:${params?.text ?? ""} cfg:${cfgVisible}`;
  },
});

const fakeProvider = (_ctx) => ({
  name: "fakeprov",
  models: ["echo-1"],
  // streamChatCompletion is the openclaw-side surface; the shim
  // adapter awaits this and consumes the returned async iterable.
  streamChatCompletion: async function* (req) {
    const last = (req.messages ?? []).filter((m) => m.role === "user").pop();
    yield { kind: "text", text: "echo: " };
    yield { kind: "text", text: last?.content ?? "" };
    yield {
      kind: "usage",
      usage: { inputTokens: (last?.content ?? "").length, outputTokens: 6 },
    };
  },
});

const fakeChannel = (_ctx) => {
  const sentReplies = [];
  return {
    name: "fakechan",
    sentReplies, // exposed so a test could introspect via the channel obj
    start({ onMessage }) {
      // Emit one canned message immediately and one after a short tick
      // so tests can verify both eager and async dispatch paths.
      onMessage({ senderId: "user-A", displayName: "User A", roomId: "room-1", text: "hello" });
      const t = setTimeout(() => {
        onMessage({ senderId: "user-B", displayName: "User B", text: "ping" });
      }, 10);
      return () => clearTimeout(t);
    },
    async send({ roomId, text }) {
      sentReplies.push({ roomId, text });
    },
  };
};

export default {
  id: "fake-tool-plugin",
  name: "Fake Tool Plugin",
  description: "Test fixture for openclaw-plugin-host (Phase 2)",
  register(api) {
    api.registerTool(fakeTool, { name: "fake-echo" });
    api.registerProvider(fakeProvider, { name: "fakeprov" });
    api.registerChannel(fakeChannel, { name: "fakechan" });
    // Exercise the captured-but-ignored path so tests can verify
    // the shim logs the warning instead of crashing.
    api.registerHttpRoute({ path: "/plugins/fake/ping" });
  },
};
