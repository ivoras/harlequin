-- Tool-result messages must carry the tool_call_id (and optionally name) of the
-- assistant tool call they answer, so the conversation replays as a valid
-- OpenAI-compatible message sequence on later turns. Without this, replayed
-- tool messages have an empty tool_call_id and providers return an empty/errored
-- completion (the agent appears to stop replying after a tool-using turn).
ALTER TABLE messages ADD COLUMN tool_call_id TEXT;
ALTER TABLE messages ADD COLUMN name TEXT;
