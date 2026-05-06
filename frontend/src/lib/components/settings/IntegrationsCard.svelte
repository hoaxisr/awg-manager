<script lang="ts">
	import type { SingboxStatus, HydraRouteStatus } from '$lib/types';
	import { Button, Modal, StatusDot } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';

	interface Props {
		singboxStatus: SingboxStatus | null;
		singboxStatusReady?: boolean;
		hydraStatus: HydraRouteStatus | null;
		singboxInstalling: boolean;
		singboxInstallError: string | null;
		oninstallSingbox: () => void;
		showSingbox?: boolean;
		showHydra?: boolean;
	}

	let {
		singboxStatus,
		singboxStatusReady = false,
		hydraStatus,
		singboxInstalling,
		singboxInstallError,
		oninstallSingbox,
		showSingbox = true,
		showHydra = true,
	}: Props = $props();

	const singboxInstalled = $derived(singboxStatus?.installed ?? false);
	const singboxRunning = $derived(singboxStatus?.running ?? false);
	const hydraInstalled = $derived(hydraStatus?.installed ?? false);
	const hydraRunning = $derived(hydraStatus?.running ?? false);

	let errorModalOpen = $state(false);

	function showErrorDetails() {
		errorModalOpen = true;
	}

	async function copyError() {
		if (singboxInstallError) {
			await copyToClipboard(singboxInstallError);
		}
	}

	// Auto-close modal when the upstream error is cleared (e.g. successful retry).
	$effect(() => {
		if (singboxInstallError === null) {
			errorModalOpen = false;
		}
	});
</script>

{#if showSingbox || showHydra}
	<div class="card">
		<div class="section-label">Интеграции</div>

		{#if showSingbox}
			<div class="setting-row">
				<div class="integration-item">
					<StatusDot
						variant={singboxInstalled && singboxRunning ? 'success' : 'muted'}
						size="md"
						ariaLabel={singboxInstalled && singboxRunning ? 'Sing-box работает' : 'Sing-box остановлен'}
					/>
					<div class="integration-meta">
						<span class="font-medium">Sing-box</span>
						{#if !singboxStatusReady}
							<span class="integration-sub">Проверяем состояние...</span>
						{:else if singboxInstalled && singboxStatus}
							<span class="integration-sub">
								v{singboxStatus.version ?? '?'}
								{#if singboxRunning && singboxStatus.pid}· pid {singboxStatus.pid}{:else if !singboxRunning}· остановлен{/if}
							</span>
							{#if !singboxRunning && singboxStatus.lastError}
								<span class="setting-description error" title={singboxStatus.lastError}>{singboxStatus.lastError}</span>
							{/if}
						{:else}
							<span class="setting-description">
								Поддержка VLESS/Reality, Hysteria2, NaiveProxy. Требует Entware на внешнем носителе.
							</span>
							{#if singboxInstallError}
								<span class="install-error-row">
									<span class="install-error-label">Не удалось установить</span>
									<Button variant="ghost" size="sm" onclick={showErrorDetails}>
										Подробнее
									</Button>
								</span>
							{/if}
						{/if}
					</div>
				</div>
				{#if !singboxStatusReady}
					<Button variant="ghost" size="sm" disabled>Проверка...</Button>
				{:else if singboxInstalled}
					<Button variant="ghost" size="sm" href="/?tab=singbox">Открыть</Button>
				{:else}
					<Button variant="primary" size="sm" onclick={oninstallSingbox} loading={singboxInstalling}>
						{singboxInstalling ? 'Установка...' : 'Установить'}
					</Button>
				{/if}
			</div>
		{/if}

		{#if showHydra}
			<div class="setting-row">
				<div class="integration-item">
					<StatusDot
						variant={hydraInstalled && hydraRunning ? 'success' : 'muted'}
						size="md"
						ariaLabel={hydraInstalled && hydraRunning ? 'HydraRoute работает' : 'HydraRoute остановлен'}
					/>
					<div class="integration-meta">
						<span class="font-medium">HydraRoute Neo</span>
						{#if hydraInstalled}
							<span class="integration-sub">{hydraRunning ? 'работает' : 'остановлен'}</span>
						{:else}
							<span class="integration-sub">не установлен</span>
						{/if}
					</div>
				</div>
				{#if hydraInstalled}
					<Button variant="ghost" size="sm" href="/routing?tab=hrneo">Открыть</Button>
				{:else}
					<Button
						variant="ghost"
						size="sm"
						href="https://github.com/Ground-Zerro/HydraRoute"
						target="_blank"
						rel="noopener noreferrer"
					>
						Установить
					</Button>
				{/if}
			</div>
		{/if}
	</div>
{/if}

<Modal
	open={errorModalOpen}
	title="Не удалось установить sing-box"
	size="lg"
	onclose={() => (errorModalOpen = false)}
>
	<pre class="error-pre">{singboxInstallError ?? ''}</pre>
	{#snippet actions()}
		<Button variant="ghost" size="sm" onclick={copyError}>Скопировать</Button>
		<Button variant="primary" size="sm" onclick={() => (errorModalOpen = false)}>
			Закрыть
		</Button>
	{/snippet}
</Modal>

<style>
	.integration-item {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		min-width: 0;
		flex: 1;
	}

	.integration-meta {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.integration-sub {
		font-size: 0.6875rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
	}

	.error {
		color: var(--color-error);
	}

	.install-error-row {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}
	.install-error-label {
		color: var(--color-error);
		font-size: 0.8125rem;
	}
	.error-pre {
		margin: 0;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		border-radius: var(--radius-sm);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 50vh;
		overflow: auto;
	}
</style>
