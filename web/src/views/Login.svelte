<script lang="ts">
  import { api, setToken, setBase, getBase } from "../lib/api";
  import { user, toast } from "../lib/stores";

  let username = $state("");
  let password = $state("");
  let base = $state(getBase());
  let showServer = $state(getBase() !== "");
  let busy = $state(false);

  async function login() {
    if (busy || !username || !password) return;
    busy = true;
    try {
      setBase(base.trim());
      const res = await api.login(username.trim(), password);
      setToken(res.token);
      user.set(res.user);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      busy = false;
    }
  }
  const onKey = (e: KeyboardEvent) => e.key === "Enter" && login();
</script>

<div class="container" style="max-width:380px; margin-top:14vh;">
  <div class="card col" style="gap:14px;">
    <div style="text-align:center;">
      <div class="brand" style="font-size:26px; color:var(--accent);">Harlequin</div>
      <div class="muted small">Sign in to your agent server</div>
    </div>
    <input placeholder="Username" autocomplete="username" bind:value={username} onkeydown={onKey} />
    <input type="password" placeholder="Password" autocomplete="current-password" bind:value={password} onkeydown={onKey} />
    {#if showServer}
      <input placeholder="Server URL (blank = same origin)" bind:value={base} onkeydown={onKey} />
    {/if}
    <button class="primary" onclick={login} disabled={busy || !username || !password}>
      {busy ? "Signing in…" : "Sign in"}
    </button>
    <button class="ghost small" onclick={() => (showServer = !showServer)}>
      {showServer ? "Hide" : "Set"} server URL
    </button>
  </div>
</div>
