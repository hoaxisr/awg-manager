<script lang="ts">
	import { Button } from '$lib/components/ui';
	import { RefreshCw } from 'lucide-svelte';
	import type { FreeTurnProcessStatus } from '$lib/types';

	interface Props {
		status?: FreeTurnProcessStatus;
		installAvailable: boolean;
	installVersion?: string;
	installedVersion?: string;
	updateAvailable?: boolean;
	remoteVersion?: string;
	remoteCheckError?: string;
	installing: boolean;
	checkingUpdates?: boolean;
		onInstall: () => void;
	onCheckUpdates?: () => void;
	}

	let {
		status,
		installAvailable,
		installVersion,
		installedVersion,
		updateAvailable = false,
		remoteVersion,
		remoteCheckError,
		installing,
		checkingUpdates = false,
		onInstall,
		onCheckUpdates
	}: Props = $props();

	const showInstall = $derived(installAvailable && status && !status.binaryPresent);
	const showUpdate = $derived(
		installAvailable && updateAvailable && status?.binaryPresent && installVersion
	);
	const showInstalled = $derived(
		installAvailable && status?.binaryPresent && !updateAvailable && (installedVersion || installVersion)
	);
</script>

{#if showInstall}
	<div class="ft-binary-warn">
		<span>
			Бинарь <code>{status?.binary}</code> не найден — awg-manager не поставляет freeturn в своём
			пакете.
		</span>
		<Button variant="secondary" size="sm" loading={installing} onclick={onInstall}>
			Установить v{installVersion} (клиент + сервер)
		</Button>
	</div>
{:else if showInstall === false && status && !status.binaryPresent && !installAvailable}
	<div class="ft-binary-warn">
		<span>
			Бинарь <code>{status.binary}</code> не найден. Установите вручную из
			<a href="https://github.com/samosvalishe/free-turn-proxy" target="_blank" rel="noopener"
				>free-turn-proxy</a>.
		</span>
	</div>
{/if}

{#if showUpdate}
	<div class="ft-binary-info">
		<span>
			Установлено v{installedVersion || '?'}. Доступно обновление до v{installVersion}.
		</span>
		<Button variant="secondary" size="sm" loading={installing} onclick={onInstall}>
			Обновить до v{installVersion}
		</Button>
	</div>
{:else if showInstalled}
	<div class="ft-binary-ok">
		<span>
			freeturn v{installedVersion || installVersion} установлен (клиент + сервер)
			{#if remoteVersion && remoteVersion !== installVersion}
				<span class="ft-muted"> · на GitHub: v{remoteVersion}</span>
			{/if}
		</span>
		{#if onCheckUpdates}
			<Button
				variant="ghost"
				size="sm"
				loading={checkingUpdates}
				onclick={onCheckUpdates}
				title="Проверить обновления на GitHub"
			>
				<RefreshCw size={14} />
				Проверить обновления
			</Button>
		{/if}
	</div>
{/if}

{#if remoteCheckError && installAvailable}
	<p class="ft-remote-warn">
		Не удалось проверить GitHub: {remoteCheckError}. Используется версия из сборки awg-manager
		(v{installVersion}).
	</p>
{/if}

{#if !status?.running && status?.lastError}
	<div class="section-label">Ошибка последнего запуска</div>
	<pre class="ft-error-box">{status.lastError}</pre>
{/if}

<style>
	.ft-binary-warn,
	.ft-binary-info {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 0.625rem;
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm);
		font-size: 0.8125rem;
		margin-bottom: 0.875rem;
	}

	.ft-binary-warn {
		border: 1px solid var(--color-warning);
		background: var(--color-warning-tint);
		color: var(--color-text-primary);
	}

	.ft-binary-info {
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
	}

	.ft-binary-ok {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 0.5rem;
		padding: 0.5rem 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 0.8125rem;
		margin-bottom: 0.875rem;
	}

	.ft-muted {
		opacity: 0.85;
	}

	.ft-remote-warn {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: -0.5rem 0 0.875rem;
	}

	.ft-binary-warn a {
		color: inherit;
		text-decoration: underline;
	}

	.ft-error-box {
		width: 100%;
		box-sizing: border-box;
		max-height: 160px;
		overflow-y: auto;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-error);
		background: var(--color-bg-secondary);
		color: var(--color-error);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		white-space: pre-wrap;
		word-break: break-all;
		margin: 0 0 0.875rem;
	}
</style>
