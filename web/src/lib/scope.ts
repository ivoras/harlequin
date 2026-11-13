// Scope names as users read them. The API's skill, memory and MCP scopes use
// "user" on the wire; the product word is "personal" (see AGENTS.md).
export const scopeLabel = (s: string): string => (s === "user" ? "personal" : s);
