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
	<div class="card">
		<h3>Подключение</h3>
		<div class="url-row">
			<code>{url}</code>
			<button type="button" class="btn btn-sm" onclick={copy}>Копировать</button>
		</div>
	</div>
{/if}

<style>
	.card { padding: 12px; border: 1px solid var(--border); border-radius: 8px; margin-bottom: 12px; }
	.url-row { display: flex; align-items: center; gap: 8px; }
	code { flex: 1; padding: 6px 10px; background: var(--bg-secondary); border-radius: 4px; overflow-x: auto; }
</style>
