<script lang="ts">
  import { onMount } from "svelte";
  import { api, setToken, setBase, getBase } from "../lib/api";
  import { user, toast } from "../lib/stores";

  // mode: "login" | "register" (collecting email+password) | "verify" (entering code)
  let mode = $state<"login" | "register" | "verify">("login");
  let email = $state("");
  let password = $state("");
  let code = $state("");
  let base = $state(getBase());
  let showServer = $state(getBase() !== "");
  let busy = $state(false);
  let canRegister = $state(false);

  // probeRegistration decides whether to offer "Create an account". Re-run
  // whenever the API base changes (not just at mount), so fixing the server
  // URL in the form updates the offer without a reload.
  async function probeRegistration() {
    try {
      setBase(base.trim());
      const r = await api.registrationEnabled();
      canRegister = r.enabled;
    } catch (e) {
      canRegister = false; // older server / unreachable; hide but say why
      console.warn("registration probe failed (register button hidden):", e);
    }
  }
  onMount(probeRegistration);

  async function login() {
    if (busy || !email || !password) return;
    busy = true;
    try {
      setBase(base.trim());
      const res = await api.login(email.trim(), password);
      setToken(res.token);
      user.set(res.user);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      busy = false;
    }
  }

  async function register() {
    if (busy || !email || !password) return;
    busy = true;
    try {
      setBase(base.trim());
      await api.register(email.trim(), password);
      mode = "verify";
      toast("We emailed you a verification code");
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      busy = false;
    }
  }

  async function verify() {
    if (busy || !code) return;
    busy = true;
    try {
      const res = await api.verify(email.trim(), code.trim());
      setToken(res.token);
      user.set(res.user);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      busy = false;
    }
  }

  function submit() {
    if (mode === "login") login();
    else if (mode === "register") register();
    else verify();
  }
  const onKey = (e: KeyboardEvent) => e.key === "Enter" && submit();
</script>

<div class="container" style="max-width:380px; margin-top:14vh;">
  <div class="card col" style="gap:14px;">
    <div style="text-align:center;">
      <div class="brand" style="font-size:26px; color:var(--accent);">Harlequin</div>
      <div class="muted small">
        {#if mode === "login"}Sign in to your agent server
        {:else if mode === "register"}Create an account
        {:else}Enter the code we emailed you{/if}
      </div>
    </div>

    {#if mode === "verify"}
      <div class="muted small">A 6-digit code was sent to <b>{email}</b>. If the server has no mail configured, check its console log.</div>
      <input placeholder="Verification code" inputmode="numeric" autocomplete="one-time-code" bind:value={code} onkeydown={onKey} />
      <button class="primary" onclick={verify} disabled={busy || !code}>
        {busy ? "Verifying…" : "Verify & sign in"}
      </button>
      <button class="ghost small" onclick={() => { mode = "register"; code = ""; }}>Back</button>
    {:else}
      <input type="email" placeholder="Email" autocomplete="username" bind:value={email} onkeydown={onKey} />
      <input type="password" placeholder={mode === "register" ? "Password (min 8 chars)" : "Password"}
        autocomplete={mode === "register" ? "new-password" : "current-password"} bind:value={password} onkeydown={onKey} />
      {#if showServer}
        <input placeholder="Server URL (blank = same origin)" bind:value={base} onkeydown={onKey}
          onchange={probeRegistration} />
      {/if}

      {#if mode === "login"}
        <button class="primary" onclick={login} disabled={busy || !email || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
        {#if canRegister}
          <button class="ghost small" onclick={() => (mode = "register")}>Create an account</button>
        {/if}
      {:else}
        <button class="primary" onclick={register} disabled={busy || !email || !password}>
          {busy ? "Sending code…" : "Register"}
        </button>
        <button class="ghost small" onclick={() => (mode = "login")}>Back to sign in</button>
      {/if}
      <button class="ghost small" onclick={() => (showServer = !showServer)}>
        {showServer ? "Hide" : "Set"} server URL
      </button>
    {/if}
  </div>
</div>
