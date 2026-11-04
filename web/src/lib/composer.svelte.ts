// Composer is the chat input's behaviour, shared by the mobile and desktop
// chat views: slash-command autocomplete (mirroring the TUI), input history
// recall, auto-grow, and submit (slash command or message to the session
// controller). Views own only markup and styling.
import { get } from "svelte/store";
import { session } from "./stores";
import { sc } from "./session.svelte";
import { matchSlash, runSlash } from "./slash";

export class Composer {
  input = $state("");
  slashSel = $state(0);
  showHelp = $state(false);
  // The textarea (bind:this); not reactive.
  el: HTMLTextAreaElement | undefined;

  // Commands matching what is being typed (reactive: reads $state).
  get suggestions() {
    return matchSlash(this.input, this.admin());
  }

  // History-recall cursor: -1 = not recalling, else index into history() (0 = newest).
  private histIdx = -1;
  // Exact text last inserted by recall, to detect edits.
  private histRecalled = "";

  constructor(private admin: () => boolean) {}

  focus(): void {
    this.el?.focus();
  }

  // resize auto-grows the textarea with its content (CSS max-height caps it).
  // Called from oninput and after programmatic changes, since setting `input`
  // from code doesn't fire the input event. rAF so the DOM has the new value.
  resize(): void {
    requestAnimationFrame(() => {
      if (!this.el) return;
      this.el.style.height = "auto";
      this.el.style.height = `${this.el.scrollHeight}px`;
    });
  }

  // setText replaces the composer content (e.g. a palette "insert command").
  setText(t: string): void {
    this.input = t;
    this.resetHistory();
    this.resize();
    this.focus();
  }

  private history(): string[] {
    const out: string[] = [];
    for (const it of sc.items) if (it.kind === "msg" && it.role === "user") out.push(it.content);
    return out.reverse();
  }

  private recall(delta: number): void {
    const hist = this.history();
    const next = this.histIdx + delta;
    if (next < -1 || next >= hist.length) return;
    this.histIdx = next;
    this.histRecalled = next === -1 ? "" : hist[next];
    this.input = this.histRecalled;
    this.resize();
  }

  private resetHistory(): void {
    this.histIdx = -1;
    this.histRecalled = "";
  }

  onInput = (e: Event): void => {
    // Any edit that diverges from the recalled text ends the recall session.
    if ((e.currentTarget as HTMLTextAreaElement).value !== this.histRecalled) this.resetHistory();
    this.resize();
  };

  // submit runs a slash command (lines starting with "/") or sends a message.
  async submit(): Promise<void> {
    const text = this.input.trim();
    if (!text) return;
    this.resetHistory();
    if (text.startsWith("/")) {
      this.input = "";
      this.slashSel = 0;
      this.resize();
      if ((await runSlash(text, this.admin())) === "help") this.showHelp = true;
      return;
    }
    if (!get(session).id) return;
    this.input = "";
    this.resize();
    sc.send(text);
  }

  answer(opt: string): void {
    this.input = opt;
    this.submit();
  }

  completeSlash = (): void => {
    const s = this.suggestions;
    if (s.length) {
      this.input = s[Math.min(this.slashSel, s.length - 1)].name + " ";
      this.slashSel = 0;
      this.focus();
    }
  };

  onKey = (e: KeyboardEvent): void => {
    const s = this.suggestions;
    if (this.slashSel >= s.length) this.slashSel = 0;
    if (s.length) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        this.slashSel = Math.min(this.slashSel + 1, s.length - 1);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        this.slashSel = Math.max(this.slashSel - 1, 0);
        return;
      }
      if (e.key === "Tab") {
        e.preventDefault();
        this.completeSlash();
        return;
      }
      // Enter accepts the highlighted suggestion, unless the typed text is
      // already exactly that command — then fall through and run it.
      if (e.key === "Enter" && !e.shiftKey && this.input.trim() !== s[this.slashSel].name) {
        e.preventDefault();
        this.completeSlash();
        return;
      }
    }
    // History recall — only when the slash menu isn't consuming the arrows,
    // and only from an empty composer or an unmodified recall, so normal
    // multiline cursor movement in typed text still works.
    const recalling = this.histIdx >= 0 && this.input === this.histRecalled;
    if (e.key === "ArrowUp" && (this.input === "" || recalling)) {
      e.preventDefault();
      if (this.input === "" && !recalling) this.resetHistory(); // stale cursor after a programmatic clear
      this.recall(1);
      return;
    }
    if (e.key === "ArrowDown" && recalling) {
      e.preventDefault();
      this.recall(-1);
      return;
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      this.submit();
    }
  };
}
