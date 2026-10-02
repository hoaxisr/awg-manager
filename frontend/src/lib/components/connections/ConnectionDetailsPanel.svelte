<script lang="ts">
	import { m } from '$lib/i18n';
	import type { ConntrackConnection } from '$lib/types';
	import { formatBytes } from '$lib/utils/format';
	import { Badge, Button } from '$lib/components/ui';
	import { X } from 'lucide-svelte';
	import { dstFqdn, routeLabel, routeVariant, normProto } from '$lib/utils/connectionsView';

	interface Props {
		conn: ConntrackConnection;
		onClose: () => void;
		onKill: () => void;
		onFilterClient: () => void;
	}

	let { conn, onClose, onKill, onFilterClient }: Props = $props();

	const fqdn = $derived(dstFqdn(conn));
	const canKill = $derived(normProto(conn.protocol) === 'tcp' || normProto(conn.protocol) === 'udp');
	const stateVariant = $derived(
		conn.state === 'ESTABLISHED' ? ('success' as const) : conn.state.startsWith('SYN') ? ('warning' as const) : ('muted' as const),
	);
</script>

<aside class="details">
	<div class="head">
		<span class="title">{m.connections_details_title()}</span>
		<button type="button" class="close" onclick={onClose} aria-label={m.sb_router_common_close()}>
			<X size={14} aria-hidden="true" />
		</button>
	</div>

	<div class="badges">
		<Badge variant="accent" size="sm">{conn.protocol.toUpperCase()}</Badge>
		{#if conn.state}<Badge variant={stateVariant} size="sm">{conn.state}</Badge>{/if}
	</div>

	<div class="kv">
		<span class="k">{m.connections_col_source()}</span><span class="v mono">{conn.src}:{conn.srcPort}</span>
		{#if conn.clientName}<span class="k">{m.connections_col_client()}</span><span class="v">{conn.clientName}</span>{/if}
		<span class="k">{m.connections_col_dst()}</span>
		<span class="v mono">{conn.dst}{#if conn.dstPort > 0}:{conn.dstPort}{/if}</span>
		{#if fqdn}<span class="k">FQDN</span><span class="v mono fqdn">{fqdn}</span>{/if}
		{#if conn.rules && conn.rules.length > 0}
			<span class="k">{m.connections_col_rule()}</span>
			<span class="v">{conn.rules.map((r) => r.listName || r.listId).join(', ')}</span>
		{/if}
		<span class="k">{m.connections_col_route()}</span>
		<span class="v"><Badge variant={routeVariant(conn)} size="sm">{routeLabel(conn)}</Badge></span>
		{#if conn.ttl > 0}
			<!-- у icmp-записей Keenetic timeout в строке отсутствует — ttl=0, прячем -->
			<span class="k">TTL</span><span class="v mono">{m.connections_ttl_seconds({ ttl: conn.ttl })}</span>
		{/if}
	</div>

	<div class="traffic mono">↑ {formatBytes(conn.bytesOut)} · ↓ {formatBytes(conn.bytesIn)}</div>

	<div class="actions">
		{#if canKill}
			<Button variant="danger" size="sm" onclick={onKill}>{m.connections_kill()}</Button>
		{/if}
		<Button variant="ghost" size="sm" onclick={onFilterClient}>{m.connections_filter_client()}</Button>
	</div>
</aside>

<style>
	.details {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-accent);
		border-radius: 6px;
		padding: 12px;
		display: grid;
		gap: 10px;
		align-content: start;
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}
	.title {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-secondary);
	}
	.close {
		all: unset;
		display: inline-flex;
		cursor: pointer;
		color: var(--color-text-muted);
		padding: 2px;
		border-radius: 4px;
	}
	.close:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
	.badges { display: flex; gap: 6px; }
	.kv {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 6px 10px;
		font-size: 12px;
		align-items: baseline;
	}
	.k { color: var(--color-text-muted); white-space: nowrap; }
	.v { min-width: 0; }
	.mono { font-family: var(--font-mono); font-size: 11px; }
	.fqdn { overflow-wrap: anywhere; }
	.traffic {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		padding: 8px 10px;
		font-size: 12px;
	}
	.actions { display: flex; gap: 8px; }

	/* 760–1100px: fixed-оверлей справа-снизу (grid-колонка давит таблицу) */
	@media (max-width: 1099px) {
		.details {
			position: fixed;
			right: 16px;
			bottom: 12px;
			width: 320px;
			max-height: 80vh;
			overflow-y: auto;
			z-index: 20;
			box-shadow: var(--shadow);
		}
	}
	/* <760px: bottom-sheet */
	@media (max-width: 760px) {
		.details {
			left: 12px;
			right: 12px;
			width: auto;
		}
	}
</style>
