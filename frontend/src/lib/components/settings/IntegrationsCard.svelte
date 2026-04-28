<script lang="ts">
	import type { SingboxStatus, HydraRouteStatus } from '$lib/types';
	import { Button, StatusDot } from '$lib/components/ui';

	interface Props {
		singboxStatus: SingboxStatus | null;
		hydraStatus: HydraRouteStatus | null;
		singboxInstalling: boolean;
		singboxInstallError: string | null;
		oninstallSingbox: () => void;
	}

	let {
		singboxStatus,
		hydraStatus,
		singboxInstalling,
		singboxInstallError,
		oninstallSingbox,
	}: Props = $props();

	const singboxInstalled = $derived(singboxStatus?.installed ?? false);
	const singboxRunning = $derived(singboxStatus?.running ?? false);
	const hydraInstalled = $derived(hydraStatus?.installed ?? false);
	const hydraRunning = $derived(hydraStatus?.running ?? false);
</script>

<div class="card">
	<div class="section-label">Интеграции</div>

	<div class="setting-row">
		<div class="integration-item">
			<StatusDot
				variant={singboxInstalled && singboxRunning ? 'success' : 'muted'}
				size="md"
				ariaLabel={singboxInstalled && singboxRunning ? 'Sing-box работает' : 'Sing-box остановлен'}
			/>
			<div class="integration-meta">
				<span class="font-medium">Sing-box</span>
				{#if singboxInstalled && singboxStatus}
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
						<span class="setting-description error">{singboxInstallError}</span>
					{/if}
				{/if}
			</div>
		</div>
		{#if singboxInstalled}
			<Button variant="ghost" size="sm" href="/?tab=singbox">Открыть</Button>
		{:else}
			<Button variant="primary" size="sm" onclick={oninstallSingbox} loading={singboxInstalling}>
				{singboxInstalling ? 'Установка...' : 'Установить'}
			</Button>
		{/if}
	</div>

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
</div>

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
</style>
