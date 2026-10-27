<!--
  Контейнер карточки «MCP-сервер» (MC2): ключи доступа живут в своём сторе,
  поэтому их подписка и выпуск/отзыв — здесь, а не в странице настроек.
  Тумблер включения меняет общие настройки — его сохраняет страница (ontoggle).
-->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { mcpKeys } from '$lib/stores/mcpKeys';
	import type { PollingState } from '$lib/stores/polling';
	import type { McpKey, McpKeyCreated } from '$lib/types';
	import McpCard from './McpCard.svelte';

	interface Props {
		enabled: boolean;
		saving?: boolean;
		origin: string;
		ontoggle: (enabled: boolean) => void;
	}

	let { enabled, saving = false, origin, ontoggle }: Props = $props();

	// Ключи запрашиваются только пока MCP включён; SSE «mcpKeys» обновляет
	// список через стор, поэтому после create/revoke руками ничего не грузим.
	let keysState = $state<PollingState<McpKey[]> | null>(null);
	$effect(() => {
		if (!enabled) {
			keysState = null;
			return;
		}
		return mcpKeys.subscribe((s) => {
			if (s.status === 'error' && keysState?.status !== 'error') {
				notifications.error(s.error ?? m.settings_page_mcp_keys_load_failed());
			}
			keysState = s;
		});
	});

	async function createKey(name: string, readOnly: boolean): Promise<McpKeyCreated> {
		const created = await api.createMcpKey(name, readOnly);
		await mcpKeys.refetch();
		return created;
	}

	async function revokeKey(id: string) {
		try {
			await api.revokeMcpKey(id);
			notifications.success(m.settings_page_key_revoked());
			await mcpKeys.refetch();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_key_revoke_failed());
		}
	}
</script>

<McpCard
	{enabled}
	{saving}
	keys={keysState?.data ?? []}
	keysLoading={keysState?.status === 'loading'}
	{origin}
	{ontoggle}
	oncreate={createKey}
	onrevoke={revokeKey}
/>
