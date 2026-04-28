// json-schema.mjs converts an openclaw tool's `parameters` field into
// a JSON-Schema bytes payload for talon's gRPC ToolSpec.
//
// openclaw extensions typically use TypeBox: every TypeBox value IS
// already a JSON Schema document (TypeBox is a JSON-Schema-compliant
// builder). So `JSON.stringify(parameters)` is the right answer.
//
// We also tolerate plain JSON objects (some test fixtures pass schema
// objects directly) and undefined (a tool with no parameters becomes
// an empty `{ "type": "object" }`).

export function toJsonSchemaBytes(parameters) {
  if (parameters == null) {
    return Buffer.from(`{"type":"object"}`, "utf8");
  }
  if (typeof parameters === "string") {
    return Buffer.from(parameters, "utf8");
  }
  if (Buffer.isBuffer(parameters)) {
    return parameters;
  }
  // Either a TypeBox object (which is itself a JSON Schema) or a plain
  // schema object. Stringify; throw if it's not serializable.
  return Buffer.from(JSON.stringify(parameters), "utf8");
}
