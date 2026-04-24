<script lang="ts">
	import { notifications } from '$lib/stores/notifications';
	import type { DeviceProxyConfig } from '$lib/types';

	interface Props {
		config: DeviceProxyConfig;
		resolvedListenIP: string;
	}

	let { config, resolvedListenIP }: Props = $props();

	let url = $derived.by(() => {
		const auth = config.auth.enabled
			? `${encodeURIComponent(config.auth.username)}:${encodeURIComponent(config.auth.password)}@`
			: '';
		return `socks5://${auth}${resolvedListenIP}:${config.port}`;
	});

	async function copy() {
		try {
			await navigator.clipboard.writeText(url);
			notifications.success('Скопировано');
		} catch {
			notifications.error('Не удалось скопировать');
		}
	}
</script>

{#if config.enabled && resolvedListenIP}
	<section class="card">
		<h2 class="section-title">Как подключиться</h2>
		<p class="section-desc">
			Вставьте этот адрес в настройки прокси на устройстве (телефон, компьютер, роутер клиента).
		</p>
		<div class="url-row">
			<code class="url-code">{url}</code>
			<button type="button" class="btn btn-ghost btn-sm" onclick={copy}>Копировать</button>
		</div>
	</section>
{/if}

<style>
	.section-title { font-size: 1rem; font-weight: 600; margin: 0 0 0.25rem 0; }
	.section-desc { font-size: 0.8125rem; color: var(--text-muted); margin: 0 0 0.75rem 0; }
	.url-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.625rem 0.75rem;
		background: var(--bg-tertiary);
		border-radius: 6px;
	}
	.url-code {
		flex: 1;
		overflow-x: auto;
		font-family: var(--font-mono, monospace);
		font-size: 0.875rem;
		white-space: nowrap;
		color: var(--text-primary);
	}
</style>
