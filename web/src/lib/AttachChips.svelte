<script lang="ts">
  // Chips for the documents attached to the prompt being written.
  import type { Composer } from "./composer.svelte";
  let { c }: { c: Composer } = $props();
</script>

{#if c.attachments.length}
  <div class="row" style="flex-wrap:wrap; gap:6px; padding:4px 0;">
    {#each c.attachments as a (a.id)}
      <span class="pill" class:err={a.state === "error"} title={a.state === "error" ? a.label : a.ref ?? a.label}>
        @{a.name}{#if a.state === "uploading"} · {a.label}{:else if a.state === "error"} · failed{/if}
        <button class="icon-btn" onclick={() => c.removeAttachment(a.id)} aria-label="Remove {a.name}">✕</button>
      </span>
    {/each}
  </div>
{/if}
