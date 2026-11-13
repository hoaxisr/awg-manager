<script lang="ts">
	import { AlertTriangle } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import type { WarpTunnelRef } from '$lib/types/warpscout';
	import { findTunnelIpv4Conflict, parseTunnelIpv4, suggestFreeTunnelIpv4 } from './warpscoutUi';

	interface Props {
		/** Введённый IPv4 туннеля как есть. */
		value: string;
		/** Известные туннели и занятые IPv4-адреса роутера. */
		targets: WarpTunnelRef[];
		usedIpv4: string[];
		/** Подсказка, когда адрес корректен и свободен. */
		hint?: string;
		/** Пользователь выбрал предложенный свободный адрес. */
		onsuggest: (ip: string) => void;
	}

	let { value, targets, usedIpv4, hint = '', onsuggest }: Props = $props();

	const valid = $derived(parseTunnelIpv4(value) !== null);
	const conflict = $derived(findTunnelIpv4Conflict(value, targets, usedIpv4));
	const suggested = $derived(
		conflict.conflict ? suggestFreeTunnelIpv4(value, targets, usedIpv4) : '',
	);
</script>

{#if value.trim() && !valid}
	<span class="ipv4-msg err">{m.warpscout_ipv4_invalid()}</span>
{:else if conflict.conflict}
	<div class="ipv4-msg warn">
		<AlertTriangle size={12} />
		<span>
			{#if conflict.tunnelName}
				{m.warpscout_ipv4_conflict_named({ ip: conflict.host, name: conflict.tunnelName })}
			{:else}
				{m.warpscout_ipv4_conflict_generic({ ip: conflict.host })}
			{/if}
		</span>
	</div>
	{#if suggested && suggested !== value.trim()}
		<div>
			<Button variant="ghost" size="sm" onclick={() => onsuggest(suggested)}>
				{m.warpscout_ipv4_use_free({ ip: suggested })}
			</Button>
		</div>
	{/if}
{:else if hint}
	<span class="ipv4-hint">{hint}</span>
{/if}

<style>
	.ipv4-hint {
		font-size: 0.69rem;
		color: var(--color-text-muted);
		line-height: 1.3;
	}
	.ipv4-msg {
		display: flex;
		align-items: flex-start;
		gap: 0.3rem;
		font-size: 0.7rem;
		line-height: 1.35;
	}
	.ipv4-msg :global(svg) {
		flex-shrink: 0;
		margin-top: 0.1rem;
	}
	.ipv4-msg.err {
		color: var(--color-error);
	}
	.ipv4-msg.warn {
		color: var(--color-warning);
	}
</style>
