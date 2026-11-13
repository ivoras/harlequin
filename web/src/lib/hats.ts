import { get } from "svelte/store";
import { api } from "./api";
import { activeProject } from "./stores";
import type { Hat } from "./types";

// Skills the given hat carries its own copies of. Empty when no hat is worn
// or the hat can't be read (the warning it feeds is advisory).
export async function hatOverlays(hat: string): Promise<string[]> {
  if (!hat) return [];
  try {
    return (await api.getHat(hat)).overlay_skills ?? [];
  } catch {
    return [];
  }
}

// Shown wherever a user opens or edits a skill their worn hat overrides: the
// hat's copy wins outright, so an edit to the normal copy is invisible.
export const hatOverrideNote = (hat: string) =>
  `Your “${hat}” hat has its own copy of this skill, and that is the copy used while you wear it. Edits here won't take effect until you take the hat off.`;

// Hats the current session can wear: a project session is shared, so the
// server refuses personal hats there — don't offer them.
export const wearableHats = (list: Hat[]): Hat[] =>
  get(activeProject) ? list.filter((h) => h.scope !== "personal") : list;
