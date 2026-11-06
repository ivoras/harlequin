<script lang="ts">
  // Owner-only account management: create, change role, reset password, delete.
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast, user } from "../lib/stores";
  import type { User } from "../lib/types";

  const roles = ["user", "admin", "owner"] as const;
  const roleHelp: Record<string, string> = {
    user: "own sessions, personal data, and projects they belong to",
    admin: "also org-wide data: shared memories and documents, skills, hats, audit log",
    owner: "everything, including managing users",
  };

  let users = $state<User[]>([]);
  let loading = $state(true);
  let filter = $state("");
  let showAdd = $state(false);
  let nEmail = $state("");
  let nRole = $state("user");
  let nPassword = $state("");
  let nSendEmail = $state(false);
  let canEmail = $state(false); // server has SMTP configured
  // Inline reset-password panel for one user.
  let resetFor = $state<number | null>(null);
  let rPassword = $state("");
  let rSendEmail = $state(false);
  // A generated password stays on screen (not a fading toast) until dismissed,
  // so the owner can copy it and hand it over.
  let issued = $state<{ email: string; password: string } | null>(null);

  const shown = $derived(
    filter.trim() ? users.filter((u) => u.email.includes(filter.trim().toLowerCase())) : users,
  );
  const owners = $derived(users.filter((u) => u.role === "owner").length);

  async function load() {
    try {
      const r = await api.listUsers();
      users = r.users ?? [];
      canEmail = r.can_email;
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      loading = false;
    }
  }
  onMount(load);

  function genPassword(): string {
    const abc = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
    const b = crypto.getRandomValues(new Uint8Array(16));
    return Array.from(b, (x) => abc[x % abc.length]).join("");
  }

  async function add() {
    const email = nEmail.trim().toLowerCase();
    const generated = !nPassword;
    const password = nPassword || genPassword();
    try {
      const r = await api.createUser({ email, password, role: nRole, send_email: nSendEmail });
      // Show a generated password unless it reached the user by e-mail.
      if (generated && !r.email_sent) issued = { email, password };
      if (r.email_error) toast(`created ${email}, but ${r.email_error}`, "error");
      else toast(r.email_sent ? `created ${email} and e-mailed the sign-in details` : `created ${email}`);
      showAdd = false;
      nEmail = nPassword = "";
      nRole = "user";
      nSendEmail = false;
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  async function setRole(u: User, role: string) {
    if (role === u.role) return;
    if (u.id === $user?.id && !confirm(`Change your own role to ${role}? You will lose access to user management.`)) {
      users = [...users]; // re-render the select back to the current role
      return;
    }
    try {
      await api.updateUser(u.id, { role });
      toast(`${u.email} is now ${role}`);
    } catch (e) {
      toast((e as Error).message, "error");
    }
    await load();
  }

  function openReset(u: User) {
    resetFor = resetFor === u.id ? null : u.id;
    rPassword = "";
    rSendEmail = false;
  }

  async function resetPassword(u: User) {
    const generated = !rPassword;
    const password = rPassword || genPassword();
    try {
      const r = await api.updateUser(u.id, { password, send_email: rSendEmail });
      if (generated && !r.email_sent) issued = { email: u.email, password };
      if (r.email_error) toast(`password reset for ${u.email}, but ${r.email_error}`, "error");
      else toast(r.email_sent ? `password reset and e-mailed to ${u.email}` : `password reset for ${u.email}`);
      resetFor = null;
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  async function del(u: User) {
    if (!confirm(`Delete ${u.email}?\n\nThis permanently removes the account and all of their personal data: sessions, memories, documents, skills and scheduled jobs. Project and shared data they contributed stays.`)) return;
    try {
      await api.deleteUser(u.id);
      users = users.filter((x) => x.id !== u.id);
      toast(`deleted ${u.email}`);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      toast("copied");
    } catch {
      toast("copy failed — select the text instead", "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="row">
      <h3>Users</h3><span class="spacer"></span>
      <button class="primary small" onclick={() => (showAdd = !showAdd)}>+ Add user</button>
    </div>

    {#if issued}
      <div class="card col">
        <div class="small">Password for <b>{issued.email}</b> — pass it on now; it won't be shown again:</div>
        <div class="row" style="gap:6px;">
          <code style="flex:1; user-select:all;">{issued.password}</code>
          <button class="small" onclick={() => copy(issued!.password)}>Copy</button>
          <button class="ghost small" onclick={() => (issued = null)} aria-label="Dismiss">✕</button>
        </div>
      </div>
    {/if}

    {#if showAdd}
      <div class="card col">
        <input placeholder="email" type="email" autocomplete="off" bind:value={nEmail} />
        <select bind:value={nRole}>
          {#each roles as r}<option value={r}>{r} — {roleHelp[r]}</option>{/each}
        </select>
        <input placeholder="password (leave empty to generate one)" type="password" autocomplete="new-password" bind:value={nPassword} />
        <label class="row small" style="gap:6px;" title={canEmail ? "" : "This server can't send e-mail (SMTP is not configured)"}>
          <input type="checkbox" bind:checked={nSendEmail} disabled={!canEmail} />
          E-mail the sign-in details (including the password) to the user{canEmail ? "" : " — unavailable: e-mail isn't configured on this server"}
        </label>
        <button class="primary" onclick={add} disabled={!nEmail.trim() || (nPassword !== "" && nPassword.length < 8)}>Create</button>
        {#if nPassword !== "" && nPassword.length < 8}<div class="muted small">At least 8 characters.</div>{/if}
      </div>
    {/if}

    {#if users.length > 8}<input placeholder="Filter by email" bind:value={filter} />{/if}

    <div class="list">
      {#each shown as u (u.id)}
        <div class="card row" style="gap:8px; flex-wrap:wrap;">
          <div class="col" style="flex:1; min-width:180px; gap:2px;">
            <div><strong>{u.email}</strong>{#if u.id === $user?.id} <span class="pill">you</span>{/if}</div>
            <div class="muted small">since {new Date(u.created_at).toLocaleDateString()}</div>
          </div>
          <select value={u.role} onchange={(e) => setRole(u, (e.currentTarget as HTMLSelectElement).value)}
            disabled={u.role === "owner" && owners <= 1} title={u.role === "owner" && owners <= 1 ? "The only owner can't be demoted" : roleHelp[u.role]}
            aria-label="Role of {u.email}">
            {#each roles as r}<option value={r}>{r}</option>{/each}
          </select>
          <button class="small" onclick={() => openReset(u)} aria-expanded={resetFor === u.id}>Reset password</button>
          {#if u.id !== $user?.id}<button class="ghost danger small" onclick={() => del(u)}>Delete</button>{/if}
          {#if resetFor === u.id}
            <div class="col" style="flex-basis:100%; gap:6px;">
              <input placeholder="new password (leave empty to generate one)" type="password" autocomplete="new-password" bind:value={rPassword} />
              <label class="row small" style="gap:6px;" title={canEmail ? "" : "This server can't send e-mail (SMTP is not configured)"}>
                <input type="checkbox" bind:checked={rSendEmail} disabled={!canEmail} />
                E-mail the new password to {u.email}{canEmail ? "" : " — unavailable: e-mail isn't configured on this server"}
              </label>
              <div class="row small" style="gap:6px;">
                <span class="muted" style="flex:1;">This signs them out everywhere.</span>
                <button class="ghost small" onclick={() => (resetFor = null)}>Cancel</button>
                <button class="primary small" onclick={() => resetPassword(u)} disabled={rPassword !== "" && rPassword.length < 8}>Reset</button>
              </div>
            </div>
          {/if}
        </div>
      {/each}
      {#if !loading && shown.length === 0}<div class="muted small">No users{filter ? " match" : ""}.</div>{/if}
    </div>
  </div>
</section>
