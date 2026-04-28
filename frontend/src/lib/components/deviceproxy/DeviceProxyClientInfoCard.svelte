<script lang="ts">
	import { notifications } from '$lib/stores/notifications';
	import { Button } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type { DeviceProxyConfig } from '$lib/types';

	interface Props {
		config: DeviceProxyConfig;
		resolvedListenIP: string;
		bridgeLabel: string;
		onOpenSettings: () => void;
	}

	let { config, resolvedListenIP, bridgeLabel, onOpenSettings }: Props = $props();

	const socksHostPort = $derived(`${resolvedListenIP}:${config.port}`);

	const socksUrl = $derived.by(() => {
		const auth = config.auth.enabled
			? `${encodeURIComponent(config.auth.username)}:${encodeURIComponent(config.auth.password)}@`
			: '';
		return `socks5://${auth}${socksHostPort}`;
	});

	const listenLabel = $derived(
		config.listenAll ? 'Все интерфейсы' : (bridgeLabel || config.listenInterface),
	);

	async function copyUrl() {
		if (await copyToClipboard(socksUrl)) {
			notifications.success('Скопировано');
		} else {
			notifications.error('Не удалось скопировать');
		}
	}
</script>

<section class="card">
	<h2 class="section-title">Подключение клиента</h2>

	<div class="info-row">
		<span class="info-key">SOCKS5</span>
		<span class="info-val">{socksHostPort}</span>
	</div>
	<div class="info-row">
		<span class="info-key">Слушать на</span>
		<span class="info-val">{listenLabel}</span>
	</div>

	<div class="actions">
		<Button variant="ghost" size="sm" onclick={copyUrl}>Копировать URL</Button>
		<Button variant="ghost" size="sm" onclick={onOpenSettings}>Настройки</Button>
	</div>
</section>

<style>
	.section-title {
		font-size: 1rem;
		font-weight: 600;
		margin: 0 0 0.75rem 0;
	}

	.info-row {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--color-border);
		gap: 1rem;
	}

	.info-row:last-of-type {
		border-bottom: none;
	}

	.info-key {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.info-val {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		text-align: right;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.actions {
		display: flex;
		gap: 0.5rem;
		margin-top: 0.875rem;
	}
</style>
