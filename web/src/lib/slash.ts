// Web slash commands for the chat composer, mirroring the TUI's: an autocomplete
// menu (filtered by role), Tab to complete, descriptions, and Enter to run.
// Commands that already have dedicated UI (the nav tabs for skills/hats/memory/
// documents/mcp/cron/config/usage, the sessions drawer for new/resume, the alert
// box buttons for dismiss/run, the queue list) are intentionally not duplicated.
import { api } from "./api";
import { sc } from "./session.svelte";
import { toast, projectSheet } from "./stores";

export interface SlashCommand {
  name: string; // e.g. "/alert"
  args?: string; // argument hint shown in the menu, e.g. "<message>"
  desc: string;
  admin?: boolean; // owner/admin only
  // run executes the command (omitted for /help, which the view handles).
  run?: (arg: string) => void | Promise<void>;
}

const commands: SlashCommand[] = [
  { name: "/help", desc: "show available commands" },
  { name: "/project", desc: "manage projects (create / invite / switch)", run: () => projectSheet.set(true) },
  { name: "/export", desc: "download the session transcript", run: () => sc.exportTranscript() },
  { name: "/clear", desc: "clear this session's context (keeps session, title, hat)", run: () => sc.clear() },
  {
    name: "/alert",
    args: "<message>",
    desc: "broadcast an alert to all users",
    admin: true,
    run: async (arg) => {
      if (!arg.trim()) return toast("usage: /alert <message>", "error");
      try {
        await api.broadcastAlert(arg.trim());
        toast("alert sent to all users");
      } catch (e) {
        toast((e as Error).message, "error");
      }
    },
  },
];

// availableCommands returns the commands visible to the user (admin-gated).
export function availableCommands(admin: boolean): SlashCommand[] {
  return commands.filter((c) => admin || !c.admin);
}

// matchSlash returns the commands matching a command being typed: a leading "/"
// with no space yet (once arguments start, the menu hides).
export function matchSlash(input: string, admin: boolean): SlashCommand[] {
  if (!input.startsWith("/") || input.includes(" ")) return [];
  return availableCommands(admin).filter((c) => c.name.startsWith(input));
}

// runSlash dispatches a typed command line. Returns "help" so the view can open
// its help panel; toasts on unknown command or insufficient role.
export async function runSlash(input: string, admin: boolean): Promise<"help" | void> {
  const sp = input.indexOf(" ");
  const name = sp === -1 ? input : input.slice(0, sp);
  const arg = sp === -1 ? "" : input.slice(sp + 1).trim();
  if (name === "/help") return "help";
  const c = commands.find((x) => x.name === name);
  if (!c) return toast("unknown command: " + name + " (try /help)", "error");
  if (c.admin && !admin) return toast(name + " is for owners and admins only", "error");
  await c.run?.(arg);
}
