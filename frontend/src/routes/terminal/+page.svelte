<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer } from '$lib/components/layout';
	import { TerminalInstall, TerminalView } from '$lib/components/terminal';
	import type { TerminalStatus } from '$lib/types';

	type PageState = 'loading' | 'not-installed' | 'starting' | 'active' | 'session-busy' | 'error';

	let pageState: PageState = $state('loading');
	let installing = $state(false);
	let installError: string | null = $state(null);

	onMount(async () => {
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
		} catch (e: any) {
			installError = e.message || 'Неизвестная ошибка';
		} finally {
			installing = false;
		}
	}

	async function startTerminal() {
		pageState = 'starting';
		try {
			await api.terminalStart();
			pageState = 'active';
		} catch (e: any) {
			notifications.error('Не удалось запустить терминал: ' + (e.message || ''));
			pageState = 'error';
		}
	}

	function handleTerminalClose() {
		api.terminalStop().catch(() => {});
	}

	function handleTerminalError(msg: string) {
		notifications.error(msg);
		pageState = 'error';
	}
</script>

<svelte:head>
	<title>Терминал — AWG Manager</title>
</svelte:head>

{#if pageState === 'loading' || pageState === 'starting'}
	<PageContainer>
		<div class="terminal-loading">
			<div class="spinner"></div>
			<p>{pageState === 'loading' ? 'Проверка...' : 'Запуск терминала...'}</p>
		</div>
	</PageContainer>
{:else if pageState === 'not-installed'}
	<PageContainer>
		<TerminalInstall {installing} error={installError} oninstall={handleInstall} />
	</PageContainer>
{:else if pageState === 'session-busy'}
	<PageContainer>
		<div class="terminal-loading">
			<p>Терминал уже открыт в другой вкладке</p>
			<button class="btn btn-primary" onclick={checkStatus}>Повторить</button>
		</div>
	</PageContainer>
{:else if pageState === 'active'}
	<div class="terminal-page">
		<TerminalView onclose={handleTerminalClose} onerror={handleTerminalError} />
	</div>
{:else}
	<PageContainer>
		<div class="terminal-loading">
			<p>Ошибка подключения к терминалу</p>
			<button class="btn btn-primary" onclick={checkStatus}>Повторить</button>
		</div>
	</PageContainer>
{/if}

<style>
	.terminal-page {
		height: calc(100vh - var(--header-height, 56px));
		padding: 0.5rem;
		box-sizing: border-box;
	}
	.terminal-loading {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		height: 60vh;
		gap: 1rem;
		color: var(--text-secondary);
	}
	.spinner {
		width: 32px;
		height: 32px;
		border: 3px solid var(--border-primary);
		border-top-color: var(--accent-primary);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}
	@keyframes spin {
		to { transform: rotate(360deg); }
	}
</style>
