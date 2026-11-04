import { api } from "./api";

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
