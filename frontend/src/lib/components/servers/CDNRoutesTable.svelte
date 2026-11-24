<script lang="ts">
	// Таблица входящих доменов диспетчера: сервисы, статус и действия по узлу.
	import { m } from '$lib/i18n';
	import type { CDNRouteRule } from '$lib/types';
	import { IconButton } from '$lib/components/ui';
	import { CheckCircle2, XCircle, Edit2, Trash2 } from 'lucide-svelte';

	interface Props {
		routes: CDNRouteRule[];
		xrayPathPrefix: string;
		ontoggle: (id: string) => void;
		onedit: (route: CDNRouteRule) => void;
		ondelete: (id: string) => void;
	}

	let { routes, xrayPathPrefix, ontoggle, onedit, ondelete }: Props = $props();
</script>

<div class="cdn-table-wrap">
	<table class="routes-table">
		<thead>
			<tr>
				<th>{m.servers_cdn_col_domain()}</th>
				<th>{m.servers_cdn_col_description()}</th>
				<th class="text-center">VLESS (XHTTP)<br><small class="text-xs text-muted">{xrayPathPrefix}/*</small></th>
				<th class="text-center">{m.servers_cdn_tg_proxy()}<br><small class="text-xs text-muted">/, /api/*</small></th>
				<th class="text-center">{m.servers_cdn_col_status()}</th>
				<th class="text-right">{m.servers_cdn_col_actions()}</th>
			</tr>
		</thead>
		<tbody>
			{#each routes as route (route.id)}
				<tr class:row-disabled={!route.enabled}>
					<td class="font-mono font-medium">
						{route.domain}
					</td>
					<td class="text-muted">
						{route.description || '—'}
					</td>
					<td class="text-center">
						{#if route.route_xray}
							<span class="badge badge-route-xray">VLESS (XHTTP)</span>
						{:else}
							<span class="text-muted">—</span>
						{/if}
					</td>
					<td class="text-center">
						{#if route.route_telegram}
							<span class="badge badge-route-tg">Telegram</span>
						{:else}
							<span class="text-muted">—</span>
						{/if}
					</td>
					<td class="text-center">
						<span class="toggle-status">
							<IconButton
								onclick={() => ontoggle(route.id)}
								title={route.enabled ? m.servers_cdn_route_disable() : m.servers_cdn_route_enable()}
								ariaLabel={route.enabled ? m.servers_cdn_route_disable() : m.servers_cdn_route_enable()}
							>
								{#if route.enabled}
									<CheckCircle2 size={18} class="text-success" />
								{:else}
									<XCircle size={18} class="text-muted" />
								{/if}
							</IconButton>
						</span>
					</td>
					<td class="text-right">
						<div class="row-actions">
							<IconButton onclick={() => onedit(route)} title={m.common_edit()} ariaLabel={m.common_edit()}>
								<Edit2 size={15} />
							</IconButton>
							<IconButton
								variant="danger"
								onclick={() => ondelete(route.id)}
								title={m.common_delete()}
								ariaLabel={m.common_delete()}
							>
								<Trash2 size={15} />
							</IconButton>
						</div>
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

<style>
	.cdn-table-wrap {
		overflow-x: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
	}

	.routes-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.875rem;
	}

	.routes-table th {
		background: var(--color-bg-tertiary);
		padding: 0.625rem 0.75rem;
		text-align: left;
		font-weight: 500;
		color: var(--color-text-secondary);
		border-bottom: 1px solid var(--color-border);
	}

	.routes-table td {
		padding: 0.625rem 0.75rem;
		color: var(--color-text-primary);
		border-bottom: 1px solid var(--color-border);
		vertical-align: middle;
	}

	.row-disabled {
		opacity: 0.55;
		background: var(--color-bg-tertiary);
	}

	.badge {
		font-size: 0.75rem;
		padding: 0.2rem 0.5rem;
		border-radius: 9999px;
		font-weight: 500;
		white-space: nowrap;
	}

	.badge-route-xray {
		background: var(--color-accent-tint);
		color: var(--color-accent);
		border: 1px solid var(--color-accent-border);
	}

	.badge-route-tg {
		background: var(--color-info-tint);
		color: var(--color-info);
		border: 1px solid var(--color-info-border);
	}

	.text-center { text-align: center; }
	.text-right { text-align: right; }
	.text-muted { color: var(--color-text-muted); }

	.toggle-status {
		display: inline-flex;
	}

	.toggle-status :global(.text-success) {
		color: var(--color-success);
	}

	.toggle-status :global(.text-muted) {
		color: var(--color-text-muted);
	}

	.row-actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.35rem;
	}
</style>
