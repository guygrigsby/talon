// Test fixture: a minimal openclaw extension that mimics the
// definePluginEntry({ register }) shape but doesn't depend on the
// openclaw runtime. The shim should treat this identically to a
// "real" bundled openclaw extension since the wire shape is the same:
// default export { register, id, name, description }.

const fakeTool = {
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
    return `echo: ${params?.text ?? ""}`;
  },
};

export default {
  id: "fake-tool-plugin",
  name: "Fake Tool Plugin",
  description: "Test fixture for openclaw-plugin-host",
  register(api) {
    api.registerTool(() => fakeTool, { name: "fake-echo" });
    // Also exercise the captured-but-ignored path so tests can verify
    // the shim logs the warning instead of crashing.
    api.registerHttpRoute({ path: "/plugins/fake/ping" });
  },
};
