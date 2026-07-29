<script lang="ts">
	import type { Snippet } from 'svelte';
	import Button from './Button.svelte';

	interface ProcessAlertStatus {
		running: boolean;
		binary: string;
		binaryPresent: boolean;
		pid?: number;
		lastError?: string;
	}

	interface Props {
		status?: ProcessAlertStatus;
		installAvailable: boolean;
		installVersion?: string;
		installedVersion?: string;
		updateAvailable?: boolean;
		installing: boolean;
		onInstall: () => void;
		/** Имя продукта для строки «… установлен»: freeturn / wdtt-client */
		productName: string;
		/** Суффикс кнопок установки/обновления и строки «установлен», напр. « (клиент + сервер)». */
		installSuffix?: string;
		/** Хвост после «Бинарь X не найден — » (когда установка доступна). */
		notFoundHint: string;
		/** Содержимое warn-блока, когда установка недоступна (разная разметка: ссылка/код). */
		manualInstall?: Snippet<[string]>;
	}

	let {
		status,
		installAvailable,
		installVersion,
		installedVersion,
		updateAvailable = false,
		installing,
		onInstall,
		productName,
		installSuffix = '',
		notFoundHint,
		manualInstall
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
	<div class="proc-alert proc-alert--warn">
		<span>
			Бинарь <code>{status?.binary}</code> не найден — {notFoundHint}
		</span>
		<Button variant="secondary" size="sm" loading={installing} onclick={onInstall}>
			Установить v{installVersion}{installSuffix}
		</Button>
	</div>
{:else if status?.running && status.binaryPresent === false}
	<div class="proc-alert proc-alert--warn">
		<span>
			Процесс ещё работает{status.pid ? ` (PID ${status.pid})` : ''}, но бинарь <code>{status.binary}</code> отсутствует
			на диске — остановите сервер и {notFoundHint}
		</span>
		{#if installAvailable && installVersion}
			<Button variant="secondary" size="sm" loading={installing} onclick={onInstall}>
				Установить v{installVersion}{installSuffix}
			</Button>
		{/if}
	</div>
{:else if showInstall === false && status && !status.binaryPresent && !installAvailable}
	<div class="proc-alert proc-alert--warn">
		{#if manualInstall}
			{@render manualInstall(status.binary)}
		{:else}
			<span>Бинарь <code>{status.binary}</code> не найден.</span>
		{/if}
	</div>
{/if}

{#if showUpdate}
	<div class="proc-alert proc-alert--info">
		<span>
			Установлено v{installedVersion || '?'}. Доступно обновление до v{installVersion}.
		</span>
		<Button variant="secondary" size="sm" loading={installing} onclick={onInstall}>
			Обновить до v{installVersion}
		</Button>
	</div>
{:else if showInstalled}
	<div class="proc-alert proc-alert--ok">
		<span>
			{productName} v{installedVersion || installVersion} установлен{installSuffix}
		</span>
	</div>
{/if}

{#if !status?.running && status?.lastError}
	<div class="section-label">Ошибка последнего запуска</div>
	<pre class="proc-alert-error">{status.lastError}</pre>
{/if}

<style>
	.proc-alert {
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

	.proc-alert--warn {
		border: 1px solid var(--color-warning);
		background: var(--color-warning-tint);
		color: var(--color-text-primary);
	}

	.proc-alert--info {
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
	}

	.proc-alert--ok {
		gap: 0.5rem;
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
	}

	.proc-alert--warn :global(a) {
		color: inherit;
		text-decoration: underline;
	}

	.proc-alert-error {
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
