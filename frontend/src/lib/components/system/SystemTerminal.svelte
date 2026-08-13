<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button } from '$lib/components/ui';
	import { TerminalInstall, TerminalView, TerminalCredentialsBar } from '$lib/components/terminal';
	import type { TerminalStatus } from '$lib/types';
	import { errorMessage } from '$lib/utils/errorMessage';
	import {
		loadTerminalAutoLogin,
		type TerminalAutoLogin,
	} from '$lib/utils/terminalCredentials';

	interface Props {
		compact?: boolean;
	}

	let { compact = false }: Props = $props();

	type PageState = 'loading' | 'not-installed' | 'starting' | 'active' | 'session-busy' | 'error';

	let pageState = $state<PageState>('loading');
	let installing = $state(false);
	let installError: string | null = $state(null);
	let autoLogin = $state<Pick<TerminalAutoLogin, 'login' | 'password'> | null>(null);

	onMount(async () => {
		autoLogin = loadTerminalAutoLogin();
		await checkStatus();
	});

	onDestroy(() => {
		if (pageState === 'active') {
			api.terminalStop().catch(() => {});
		}
	});

	async function checkStatus() {
		try {
			const status: TerminalStatus = await api.terminalStatus();
			if (!status.installed) {
				pageState = 'not-installed';
			} else if (status.sessionActive) {
				pageState = 'session-busy';
			} else {
				await startTerminal();
			}
		} catch {
			pageState = 'error';
		}
	}

	async function handleInstall() {
		installing = true;
		installError = null;
		try {
			await api.terminalInstall();
			notifications.success('ttyd установлен');
			await startTerminal();
		} catch (e) {
			installError = errorMessage(e, 'Неизвестная ошибка');
		} finally {
			installing = false;
		}
	}

	async function startTerminal() {
		pageState = 'starting';
		try {
			await api.terminalStart();
			pageState = 'active';
		} catch (e) {
			notifications.error('Не удалось запустить терминал: ' + errorMessage(e, ''));
			pageState = 'error';
		}
	}

	function handleTerminalClose() {
		api.terminalStop().catch(() => {});
	}

	async function handleTerminalReconnect() {
		await api.terminalStart();
	}

	function handleTerminalError(msg: string) {
		notifications.error(msg);
		pageState = 'error';
	}
</script>

<div class="system-terminal" class:compact>
	{#if !compact}
		<TerminalCredentialsBar onchange={(v) => (autoLogin = v)} />
	{/if}

	{#if pageState === 'loading' || pageState === 'starting'}
		<p class="muted">Запуск терминала…</p>
	{:else if pageState === 'not-installed'}
		<TerminalInstall {installing} error={installError} oninstall={handleInstall} />
	{:else if pageState === 'session-busy'}
		<p>Терминал уже открыт в другой вкладке.</p>
		<Button variant="secondary" onclick={checkStatus}>Проверить снова</Button>
	{:else if pageState === 'active'}
		<div class="term-wrap" class:compact>
			<TerminalView
				{autoLogin}
				{compact}
				onclose={handleTerminalClose}
				onerror={handleTerminalError}
				onreconnect={handleTerminalReconnect}
			/>
		</div>
	{:else}
		<p>Не удалось запустить терминал.</p>
		<Button variant="secondary" onclick={checkStatus}>Повторить</Button>
	{/if}
</div>

<style>
	.system-terminal {
		min-height: 420px;
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}
	.system-terminal.compact {
		min-height: 240px;
	}
	.term-wrap {
		height: min(70vh, 560px);
		border: 1px solid var(--border-subtle, #333);
		border-radius: 8px;
		overflow: hidden;
	}
	.term-wrap.compact {
		height: min(40vh, 320px);
	}
	.muted {
		opacity: 0.7;
	}
</style>
