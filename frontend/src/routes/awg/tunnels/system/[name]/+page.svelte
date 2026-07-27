<script lang="ts">
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import { goto } from '$app/navigation';
	import { notifications } from '$lib/stores/notifications';
	import type { SystemTunnel, ASCParams } from '$lib/types';
	import { PageContainer } from '$lib/components/layout';
	import { ASCEditor } from '$lib/components/asc';
	import { ArrowLeft } from 'lucide-svelte';
	import { Button } from '$lib/components/ui';
	import { formatBytes } from '$lib/utils/format';

	const name = $page.params.name!;

	let tunnel = $state<SystemTunnel | null>(null);
	let ascParams = $state<ASCParams | null>(null);
	let saving = $state(false);
	let loading = $state(true);
	let error = $state<string | null>(null);

	$effect(() => {
		loadData();
	});

	async function loadData() {
		loading = true;
		error = null;
		try {
			const [t, asc] = await Promise.all([
				api.getSystemTunnel(name),
				api.getASCParams(name),
			]);
			tunnel = t;
			ascParams = asc;
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось загрузить данные';
		} finally {
			loading = false;
		}
	}

	async function handleSave() {
		if (!ascParams) return;
		saving = true;
		try {
			await api.setASCParams(name, ascParams);
			notifications.success('Параметры обфускации сохранены');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения');
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head>
	<title>{tunnel?.description || name} — AWG Manager</title>
</svelte:head>

<PageContainer width="wide">
	<div class="edit-wrapper">
		<div class="sticky-header">
		<div class="header-left">
			<Button variant="ghost" size="sm" onclick={() => goto('/awg/tunnels')} iconBefore={backIcon}>
				Назад
			</Button>
			<h1 class="page-title">{tunnel?.description || name}</h1>
			<span class="badge-system">Системный</span>
		</div>
		<Button
			variant="primary"
			size="md"
			onclick={handleSave}
			disabled={!ascParams}
			loading={saving}
		>
			{saving ? 'Сохранение...' : 'Сохранить'}
		</Button>
	</div>

	{#if loading}
		<div class="py-12 text-center text-surface-400">Загрузка...</div>
	{:else if error}
		<div class="py-12 text-center text-error-500">{error}</div>
	{:else if tunnel && ascParams}
		<div class="section">
			<h2 class="section-title">Информация</h2>
			<div class="info-grid">
				<div class="info-item">
					<span class="info-label">Статус</span>
					<span class="info-value">{tunnel.status === 'up' ? 'Активен' : 'Неактивен'}</span>
				</div>
				<div class="info-item">
					<span class="info-label">Интерфейс</span>
					<span class="info-value font-mono">{tunnel.interfaceName}</span>
				</div>
				<div class="info-item">
					<span class="info-label">MTU</span>
					<span class="info-value">{tunnel.mtu}</span>
				</div>
				{#if tunnel.peer?.endpoint}
					<div class="info-item">
						<span class="info-label">Endpoint</span>
						<span class="info-value font-mono">{tunnel.peer.endpoint}</span>
					</div>
				{/if}
				{#if tunnel.peer?.publicKey}
					<div class="info-item">
						<span class="info-label">Public Key</span>
						<span class="info-value font-mono text-xs">{tunnel.peer.publicKey}</span>
					</div>
				{/if}
				{#if tunnel.peer}
					<div class="info-item">
						<span class="info-label">Трафик</span>
						<span class="info-value"
							>RX: {formatBytes(tunnel.peer.rxBytes)} / TX: {formatBytes(tunnel.peer.txBytes)}</span
						>
					</div>
				{/if}
			</div>
		</div>

		<div class="tab-content">
			<ASCEditor bind:params={ascParams} mtu={tunnel.mtu} idPrefix="sys-" />
		</div>
	{/if}
	</div>
</PageContainer>

{#snippet backIcon()}
	<ArrowLeft size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<style>
	.sticky-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		position: sticky;
		top: 0;
		z-index: 10;
		background: var(--bg-primary);
		padding: 0.75rem 0;
		margin-bottom: 1rem;
		border-bottom: 1px solid var(--border);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.page-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.badge-system {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 0.6875rem;
		font-weight: 500;
		border-radius: 9999px;
		background: rgba(148, 163, 184, 0.15);
		color: var(--text-muted);
	}

	.section {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 1.25rem;
		margin-bottom: 1rem;
		overflow: visible;
	}

	.section-title {
		font-size: 1rem;
		font-weight: 600;
		margin: 0 0 1rem;
	}

	.info-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
	}

	.info-item {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.info-label {
		font-size: 0.6875rem;
		text-transform: uppercase;
		color: var(--text-muted);
	}

	.info-value {
		font-size: 0.875rem;
		color: var(--text-primary);
	}

	@media (max-width: 640px) {
		.info-grid {
			grid-template-columns: 1fr;
		}

		.sticky-header {
			flex-direction: column;
			gap: 0.75rem;
			align-items: stretch;
		}

		.header-left {
			flex-wrap: wrap;
		}
	}
</style>
