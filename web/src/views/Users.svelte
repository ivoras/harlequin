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
  // A generated password stays on screen (not a fading toast) until dismissed,
  // so the owner can copy it and hand it over.
  let issued = $state<{ email: string; password: string } | null>(null);

  const shown = $derived(
    filter.trim() ? users.filter((u) => u.email.includes(filter.trim().toLowerCase())) : users,
  );
  const owners = $derived(users.filter((u) => u.role === "owner").length);

  async function load() {
    try {
      users = await api.listUsers();
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
      await api.createUser({ email, password, role: nRole });
      if (generated) issued = { email, password };
      toast(`created ${email}`);
      showAdd = false;
      nEmail = nPassword = "";
      nRole = "user";
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

  async function resetPassword(u: User) {
    const typed = prompt(`New password for ${u.email} (at least 8 characters).\nLeave empty to generate one.\nThis signs them out everywhere.`);
    if (typed === null) return;
    const password = typed || genPassword();
    try {
      await api.updateUser(u.id, { password });
      if (!typed) issued = { email: u.email, password };
      toast(`password reset for ${u.email}`);
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
          <button class="small" onclick={() => resetPassword(u)}>Reset password</button>
          {#if u.id !== $user?.id}<button class="ghost danger small" onclick={() => del(u)}>Delete</button>{/if}
        </div>
      {/each}
      {#if !loading && shown.length === 0}<div class="muted small">No users{filter ? " match" : ""}.</div>{/if}
    </div>
  </div>
</section>
