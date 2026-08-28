<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card } from '$lib/components/ui';
	import { TerminalInstall, TerminalView, TerminalCredentialsBar } from '$lib/components/terminal';
	import type { TerminalStatus } from '$lib/types';
	import { errorMessage } from '$lib/utils/errorMessage';
	import {
		loadTerminalAutoLogin,
		type TerminalAutoLogin,
	} from '$lib/utils/terminalCredentials';
	import { Terminal, Play, Square, RefreshCw } from 'lucide-svelte';

	interface Props {
		compact?: boolean;
	}

	let { compact = false }: Props = $props();

	type PageState = 'loading' | 'not-installed' | 'idle' | 'starting' | 'active' | 'session-busy' | 'error';

	let pageState = $state<PageState>('loading');
	let installing = $state(false);
	let installError: string | null = $state(null);
	let autoLogin = $state<Pick<TerminalAutoLogin, 'login' | 'password'> | null>(null);
	// ownedSession is true only when THIS component started the ttyd session.
	// Used to decide whether onDestroy should stop the session: when another
	// tab already opened ttyd, leaving the System tab must not kill it.
	let ownedSession = $state(false);

	onMount(async () => {
		autoLogin = loadTerminalAutoLogin();
		await checkStatus();
	});

	onDestroy(() => {
		if (ownedSession && pageState === 'active') {
			ownedSession = false;
			api.terminalStop().catch(() => {});
		}
	});

	async function checkStatus() {
		try {
			const status: TerminalStatus = await api.terminalStatus();
			if (!status.installed) {
				pageState = 'not-installed';
			} else if (status.sessionActive) {
				pageState = 'active';
			} else {
				pageState = compact ? 'idle' : 'active';
				if (!compact) {
					await startTerminal();
				}
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
			ownedSession = true;
			pageState = 'active';
		} catch (e) {
			notifications.error('Не удалось запустить терминал: ' + errorMessage(e, ''));
			pageState = 'error';
		}
	}

	async function stopTerminal() {
		try {
			await api.terminalStop();
			ownedSession = false;
			pageState = 'idle';
			notifications.info('Сессия терминала остановлена');
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка остановки'));
		}
	}

	function handleTerminalClose() {
		ownedSession = false;
		pageState = 'idle';
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
	<!-- Terminal Control Toolbar -->
	{#if !compact}
		<div class="term-ctrl-bar">
			<div class="term-status-pill" class:active={pageState === 'active'}>
				<span class="dot"></span>
				<span>{pageState === 'active' ? 'Сессия активна (ttyd)' : 'Терминал остановлен'}</span>
			</div>

			<div class="term-actions">
				{#if pageState === 'active'}
					<Button size="sm" variant="danger" onclick={stopTerminal}>
						{#snippet iconBefore()}<Square size={13} />{/snippet}
						Остановить терминал
					</Button>
				{:else if pageState === 'idle'}
					<Button size="sm" variant="primary" onclick={startTerminal}>
						{#snippet iconBefore()}<Play size={13} />{/snippet}
						Запустить терминал
					</Button>
				{/if}
			</div>
		</div>

		<TerminalCredentialsBar onchange={(v) => (autoLogin = v)} />
	{/if}

	{#if pageState === 'loading' || pageState === 'starting'}
		<div class="term-placeholder">
			<RefreshCw size={24} class="spin" />
			<p>Запуск сессии терминала…</p>
		</div>
	{:else if pageState === 'not-installed'}
		<TerminalInstall {installing} error={installError} oninstall={handleInstall} />
	{:else if pageState === 'session-busy'}
		<div class="term-placeholder">
			<p>Терминал уже открыт в другой сессии.</p>
			<Button variant="secondary" onclick={checkStatus}>Проверить снова</Button>
		</div>
	{:else if pageState === 'idle'}
		<div class="term-placeholder">
			<Terminal size={36} class="muted" />
			<div class="idle-txt">
				<h3>Терминал выключен</h3>
				<p>Процесс ttyd остановлен и не потребляет ресурсы роутера.</p>
			</div>
			<Button variant="primary" onclick={startTerminal}>
				{#snippet iconBefore()}<Play size={14} />{/snippet}
				Запустить сессию
			</Button>
		</div>
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
		<div class="term-placeholder">
			<p>Не удалось запустить терминал.</p>
			<Button variant="secondary" onclick={checkStatus}>Повторить</Button>
		</div>
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

	.term-ctrl-bar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 0.4rem 0.6rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
	}

	.term-status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--color-text-muted);
	}
	.term-status-pill .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: #94a3b8;
	}
	.term-status-pill.active {
		color: var(--color-success, #34d399);
	}
	.term-status-pill.active .dot {
		background: var(--color-success, #22c55e);
		box-shadow: 0 0 6px var(--color-success, #22c55e);
	}

	.term-placeholder {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		text-align: center;
		height: 320px;
		gap: 0.75rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		padding: 2rem;
	}
	.idle-txt h3 {
		margin: 0;
		font-size: 1rem;
		color: var(--color-text-primary);
	}
	.idle-txt p {
		margin: 0.2rem 0 0 0;
		font-size: 0.82rem;
		color: var(--color-text-muted);
	}

	.term-wrap {
		height: min(70vh, 560px);
		border: 1px solid var(--color-border, #333);
		border-radius: 8px;
		overflow: hidden;
	}
	.term-wrap.compact {
		height: min(40vh, 320px);
	}
	.muted {
		opacity: 0.5;
	}
</style>
