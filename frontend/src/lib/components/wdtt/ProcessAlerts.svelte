<script lang="ts">
	import { Button } from '$lib/components/ui';
	import type { WdttProcessStatus } from '$lib/types';

	interface Props {
		status?: WdttProcessStatus;
		installAvailable: boolean;
		installVersion?: string;
		installedVersion?: string;
		updateAvailable?: boolean;
		installing: boolean;
		onInstall: () => void;
	}

	let {
		status,
		installAvailable,
		installVersion,
		installedVersion,
		updateAvailable = false,
		installing,
		onInstall
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
	<div class="wdtt-binary-warn">
		<span>
			Бинарь <code>{status?.binary}</code> не найден — установите wdtt-client из IPK или prebuilt.
		</span>
		<Button variant="secondary" size="sm" loading={installing} onclick={onInstall}>
			Установить v{installVersion}
		</Button>
	</div>
{:else if showInstall === false && status && !status.binaryPresent && !installAvailable}
	<div class="wdtt-binary-warn">
		<span>
			Бинарь <code>{status.binary}</code> не найден. Соберите
			<code>scripts/build-wdtt-client.sh</code> для вашей архитектуры.
		</span>
	</div>
{/if}

{#if showUpdate}
	<div class="wdtt-binary-info">
		<span>
			Установлено v{installedVersion || '?'}. Доступно обновление до v{installVersion}.
		</span>
		<Button variant="secondary" size="sm" loading={installing} onclick={onInstall}>
			Обновить до v{installVersion}
		</Button>
	</div>
{:else if showInstalled}
	<div class="wdtt-binary-ok">
		<span>wdtt-client v{installedVersion || installVersion} установлен</span>
	</div>
{/if}

{#if !status?.running && status?.lastError}
	<div class="section-label">Ошибка последнего запуска</div>
	<pre class="wdtt-error-box">{status.lastError}</pre>
{/if}

<style>
	.wdtt-binary-warn,
	.wdtt-binary-info {
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

	.wdtt-binary-warn {
		border: 1px solid var(--color-warning);
		background: var(--color-warning-tint);
	}

	.wdtt-binary-info,
	.wdtt-binary-ok {
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 0.8125rem;
		margin-bottom: 0.875rem;
		padding: 0.5rem 0.75rem;
		border-radius: var(--radius-sm);
	}

	.wdtt-error-box {
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
