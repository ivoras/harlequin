// Async document upload with live progress: starts a server-side ingest job
// and polls it, reporting stage / percent / elapsed seconds to the caller's
// callback until the job finishes. Both the Documents view and the Projects
// tab render the same indicator from these updates.
import { api } from "./api";
import type { Document } from "./types";

export interface IngestProgress {
  stage: string; // "uploading" | "extracting" | "describing" | "embedding" | …
  pct: number | null; // 0-100 when the stage reports chunk counts, else null
  elapsed: number; // whole seconds since the upload started
}

const POLL_MS = 1000;

// uploadWithProgress resolves with the ingested document, or rejects with the
// job's error. onUpdate fires at least once a second (ticker + poll results).
export async function uploadWithProgress(
  file: File,
  opts: { title?: string; scope?: string; projectID?: number },
  onUpdate: (p: IngestProgress) => void,
): Promise<Document> {
  const started = Date.now();
  let last: IngestProgress = { stage: "uploading", pct: null, elapsed: 0 };
  const emit = (stage: string, pct: number | null) => {
    last = { stage, pct, elapsed: Math.floor((Date.now() - started) / 1000) };
    onUpdate(last);
  };
  emit("uploading", null);
  // Tick the elapsed counter between polls so the display visibly moves.
  const ticker = setInterval(() => onUpdate({ ...last, elapsed: Math.floor((Date.now() - started) / 1000) }), 1000);
  try {
    const jobID = await api.uploadDocumentAsync(file, opts.title, opts.scope, opts.projectID);
    for (;;) {
      const st = await api.getIngestJob(jobID);
      if (st.finished) {
        if (st.error) throw new Error(st.error);
        if (!st.document) throw new Error("ingestion finished without a document");
        return st.document;
      }
      const pct = st.stage === "embedding" && st.total ? Math.floor(((st.done ?? 0) * 100) / st.total) : null;
      emit(st.stage, pct);
      await new Promise((r) => setTimeout(r, POLL_MS));
    }
  } finally {
    clearInterval(ticker);
  }
}

// ingestLabel renders one progress update as the indicator text, e.g.
// "extracting… 47s" or "embedding… 62% · 1m 12s".
export function ingestLabel(p: IngestProgress): string {
  const t = p.elapsed >= 60 ? `${Math.floor(p.elapsed / 60)}m ${p.elapsed % 60}s` : `${p.elapsed}s`;
  return p.pct !== null ? `${p.stage}… ${p.pct}% · ${t}` : `${p.stage}… ${t}`;
}
