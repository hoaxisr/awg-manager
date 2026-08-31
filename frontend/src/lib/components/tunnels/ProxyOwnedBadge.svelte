<script lang="ts" module>
	import type { TunnelListItem } from '$lib/types';

	/**
	 * Ключ инстанса прокси, которому принадлежит туннель, либо пустая строка.
	 *
	 * Туннель создаёт мастер «Выхода» и ручка ensure-wg: его `Endpoint` смотрит
	 * на локальный порт прокси-процесса, а поле связи проставляет бэкенд
	 * (`storage.AWGTunnel.WdttClientID` / `FreeTurnClientID`). Такой туннель —
	 * не самостоятельный выход: его поднимает и опускает прокси-инстанс, а
	 * удаление инстанса уносит и его.
	 */
	export function proxyOwnerKey(t: TunnelListItem): string {
		const wd = t.wdttClientId?.trim();
		if (wd) return `wdtt-client:${wd}`;
		const ft = t.freeTurnClientId?.trim();
		if (ft) return `freeturn-client:${ft}`;
		return '';
	}
</script>

<script lang="ts">
	// Метка «этот туннель принадлежит прокси» и ссылка на его владельца.
	// Прежде такой туннель стоял в списке наравне с обычными, и человек видел
	// два объекта на один выход, не понимая, откуда взялся opkgtunN.
	import { ExternalLink } from 'lucide-svelte';
	import { Badge } from '$lib/components/ui';

	interface Props {
		tunnel: TunnelListItem;
	}

	let { tunnel }: Props = $props();

	const ownerKey = $derived(proxyOwnerKey(tunnel));
</script>

{#if ownerKey}
	<a
		class="proxy-owned"
		href={`/proxy?tab=exit&instance=${encodeURIComponent(ownerKey)}`}
		title="Туннель создан прокси-выходом: поднимается и удаляется вместе с ним"
	>
		<Badge size="xs" variant="purple">прокси</Badge>
		<ExternalLink size={11} />
	</a>
{/if}

<style>
	.proxy-owned {
		display: inline-flex;
		align-items: center;
		gap: 0.2rem;
		text-decoration: none;
		color: var(--color-text-muted);
	}

	.proxy-owned:hover {
		color: var(--color-text-primary);
	}
</style>
