<script lang="ts">
  import type { Snippet } from "svelte";
  import { activeProject } from "../lib/stores";
  import Icon from "./Icon.svelte";
  import { ui, MOD } from "./state.svelte";

  // The main pane's header: sidebar re-open (when hidden), workspace crumb,
  // page title/controls, page actions, and the inspector toggle.
  let { title, actions, label = "" }: { title?: Snippet; actions?: Snippet; label?: string } = $props();
</script>

<header class="topbar">
  {#if !ui.sidebarOpen}
    <button class="icon-btn" onclick={() => (ui.sidebarOpen = true)} title="Show sidebar ({MOD}+B)" aria-label="Show sidebar">
      <Icon name="panelLeft" />
    </button>
  {/if}
  <span class="crumb">{$activeProject ? $activeProject.name : "Personal"}</span>
  <span class="crumb faint">/</span>
  {#if title}{@render title()}{:else}<span class="page-title">{label}</span>{/if}
  <span class="spacer"></span>
  {#if actions}{@render actions()}{/if}
  <button class="icon-btn" class:on={ui.inspectorOpen} onclick={() => (ui.inspectorOpen = !ui.inspectorOpen)}
    title="Toggle side panel ({MOD}+.)" aria-label="Toggle side panel">
    <Icon name="panelRight" />
  </button>
</header>
