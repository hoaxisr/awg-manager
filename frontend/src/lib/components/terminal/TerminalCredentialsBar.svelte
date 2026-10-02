<script lang="ts">
	import { m } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { ChevronDown } from 'lucide-svelte';
	import { Button, Card, Toggle } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import {
		clearTerminalAutoLogin,
		loadTerminalAutoLogin,
		saveTerminalAutoLogin,
		type TerminalAutoLogin,
	} from '$lib/utils/terminalCredentials';

	interface Props {
		onchange?: (value: TerminalAutoLogin | null) => void;
	}

	let { onchange }: Props = $props();

	let login = $state('');
	let password = $state('');
	let remember = $state(false);
	let saved = $state(false);
	let open = $state(true);

	onMount(() => {
		const stored = loadTerminalAutoLogin();
		if (stored) {
			login = stored.login;
			password = stored.password;
			remember = true;
			saved = true;
			open = false;
			onchange?.(stored);
		}
	});

	function emit() {
		const value = remember && login.trim()
			? { login: login.trim(), password, enabled: true }
			: null;
		onchange?.(value);
	}

	function handleSave() {
		if (!login.trim()) {
			notifications.error(m.terminal_creds_need_login());
			return;
		}
		const payload: TerminalAutoLogin = {
			login: login.trim(),
			password,
			enabled: remember,
		};
		if (remember) {
			saveTerminalAutoLogin(payload);
			saved = true;
			notifications.success(m.terminal_creds_saved_toast());
		} else {
			clearTerminalAutoLogin();
			saved = false;
		}
		emit();
	}

	function handleClear() {
		login = '';
		password = '';
		remember = false;
		saved = false;
		clearTerminalAutoLogin();
		onchange?.(null);
		notifications.success(m.terminal_creds_removed_toast());
	}
</script>

<Card padding="sm">
	<details bind:open>
	<summary class="head">
		<h3>{m.terminal_creds_title()}</h3>
		{#if saved}
			<span class="badge">{m.terminal_creds_saved()}</span>
		{/if}
		<span class="chevron" aria-hidden="true"><ChevronDown size={16} strokeWidth={2} /></span>
	</summary>

	<p class="hint">
		{m.terminal_creds_hint_main()}
		(<code>ssh</code>, <code>sudo</code>) {m.terminal_creds_hint_tail()}
	</p>

	<div class="form">
		<label>
			<span>{m.terminal_creds_login()}</span>
			<input type="text" bind:value={login} autocomplete="username" placeholder="root" />
		</label>
		<label>
			<span>{m.terminal_creds_password()}</span>
			<input bind:value={password} type="password" autocomplete="current-password" />
		</label>
		<div class="remember">
			<Toggle checked={remember} onchange={(v) => (remember = v)} />
			<span>{m.terminal_creds_remember()}</span>
		</div>
	</div>

	<div class="actions">
		<Button variant="primary" onclick={handleSave}>{m.sb_router_common_save()}</Button>
		<Button variant="ghost" onclick={handleClear}>{m.terminal_creds_clear()}</Button>
	</div>
	</details>
</Card>

<style>
	.head {
		display: flex;
		gap: 0.5rem;
		align-items: center;
		cursor: pointer;
		list-style: none;
	}
	.head::-webkit-details-marker {
		display: none;
	}
	details[open] .head {
		margin-bottom: 0.75rem;
	}
	.chevron {
		display: inline-flex;
		flex-shrink: 0;
		color: var(--color-text-muted);
		transition: transform var(--t-fast) ease;
	}
	details[open] .chevron {
		transform: rotate(180deg);
	}
	h3 {
		margin: 0;
		margin-right: auto;
		font-size: 0.95rem;
	}
	.hint {
		margin: 0 0 0.65rem;
		font-size: 0.8rem;
		opacity: 0.75;
		max-width: 52rem;
	}
	.badge {
		font-size: 0.75rem;
		padding: 0.15rem 0.5rem;
		border-radius: 999px;
		background: color-mix(in srgb, var(--success, #22c55e) 18%, transparent);
		color: var(--success, #22c55e);
		white-space: nowrap;
	}
	.form {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
		gap: 0.65rem;
		margin-bottom: 0.65rem;
	}
	label {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.8rem;
	}
	label span {
		opacity: 0.8;
	}
	input[type='text'],
	input[type='password'] {
		padding: 0.4rem 0.5rem;
	}
	.remember {
		flex-direction: row;
		align-items: center;
		gap: 0.45rem;
		align-self: end;
	}
	.actions {
		display: flex;
		gap: 0.35rem;
		flex-wrap: wrap;
	}
</style>
