<script lang="ts">
	import { onMount } from 'svelte';
	import { Button, Card } from '$lib/components/ui';
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

	onMount(() => {
		const stored = loadTerminalAutoLogin();
		if (stored) {
			login = stored.login;
			password = stored.password;
			remember = true;
			saved = true;
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
			notifications.error('Укажите логин');
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
			notifications.success('Данные для автовхода сохранены на время сессии');
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
		notifications.success('Автовход отключён');
	}
</script>

<Card padding="sm">
	<div class="head">
		<div>
			<h3>Автовход в shell</h3>
			<p class="hint">
				Логин и пароль сохраняются только на время сессии браузера (в памяти вкладки) и автоматически подставляются при запросе
				<code>login</code> / <code>Password</code> в терминале.
			</p>
		</div>
		{#if saved}
			<span class="badge">Сохранено</span>
		{/if}
	</div>

	<div class="form">
		<label>
			<span>Логин</span>
			<input type="text" bind:value={login} autocomplete="username" placeholder="root" />
		</label>
		<label>
			<span>Пароль</span>
			<input bind:value={password} type="password" autocomplete="current-password" />
		</label>
		<label class="remember">
			<input type="checkbox" bind:checked={remember} />
			<span>Запомнить на время сессии</span>
		</label>
	</div>

	<div class="actions">
		<Button variant="primary" onclick={handleSave}>Сохранить</Button>
		<Button variant="ghost" onclick={handleClear}>Очистить</Button>
	</div>
</Card>

<style>
	.head {
		display: flex;
		justify-content: space-between;
		gap: 0.75rem;
		align-items: flex-start;
		margin-bottom: 0.75rem;
	}
	h3 {
		margin: 0 0 0.25rem;
		font-size: 0.95rem;
	}
	.hint {
		margin: 0;
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
