<script lang="ts" module>
	import { m } from '$lib/i18n';

	export interface RouteOption {
		id: string;
		/** `host:port`; у Reality без известного хоста — `:port`. */
		domain: string;
		/** Подпись, заданная пользователем (имя эндпоинта, описание маршрута); пусто — подбираем по домену. */
		description: string;
		mode?: string;
	}

	/** Домен для показа: у Reality без хоста подставляем «белый IP». */
	export function shownDomain(domain: string): string {
		return domain.startsWith(':') ? `${m.servers_xray_share_white_ip()}${domain}` : domain;
	}

	export function modeLabel(mode?: string): string {
		if (mode === 'finiteproto') return 'FiniteProto v2';
		if (mode === 'reality-tcp') return 'Reality · TCP';
		if (mode === 'reality-xhttp') return 'Reality · XHTTP';
		return 'XHTTP';
	}

	export function routeDescription(opt: RouteOption): string {
		const d = (opt.description || '').trim();
		if (d) return d;
		if (opt.id === 'reality') return m.servers_xray_share_reality_route();
		const dom = (opt.domain || '').toLowerCase();
		if (dom.includes('edge.') || dom.includes('cdn.') || dom.includes('selcdn') || dom.includes('twcdn')) {
			return m.servers_xray_share_via_cdn();
		}
		if (dom.includes('netcraze.club') || dom.includes('crazedns') || dom.includes('keenetic.')) {
			return 'KeenDNS';
		}
		return m.servers_xray_share_direct_address();
	}
</script>

<script lang="ts">
	import type { XrayClient } from '$lib/types';

	interface Props {
		clients: XrayClient[];
		clientId: string;
		routes: RouteOption[];
		selectedEndpointId: string;
		selectedDomain: string;
		isPrimaryFinite: boolean;
		onclientchange: (id: string) => void;
		onroutechange: (val: string) => void;
	}

	let {
		clients,
		clientId,
		routes,
		selectedEndpointId,
		selectedDomain,
		isPrimaryFinite,
		onclientchange,
		onroutechange
	}: Props = $props();
</script>

{#if clients.length > 1}
	<div class="endpoint-picker">
		<span class="picker-label">{m.servers_xray_share_device_label()}</span>
		<select class="picker-select" value={clientId} onchange={(e) => onclientchange(e.currentTarget.value)}>
			{#each clients as c (c.id)}
				<option value={c.id}>
					{c.remark || m.servers_xray_device_fallback()}{c.enabled ? '' : m.servers_xray_share_device_off_suffix()}
				</option>
			{/each}
		</select>
	</div>
{/if}
{#if routes.length > 1}
	<div class="endpoint-picker">
		<span class="picker-label">{m.servers_xray_share_route_label()}</span>
		<div class="route-pills">
			{#each routes as opt (opt.id || opt.domain)}
				<button
					type="button"
					class="route-pill"
					class:active={(selectedEndpointId || selectedDomain) === (opt.id || opt.domain)}
					onclick={() => onroutechange(opt.id || opt.domain)}
				>
					<span class="route-pill-title">{routeDescription(opt)}</span>
					<span class="route-pill-sub font-mono">
						{shownDomain(opt.domain)} · {modeLabel(opt.mode)}
					</span>
				</button>
			{/each}
		</div>
	</div>
{:else if routes.length === 1}
	<div class="endpoint-picker">
		<span class="picker-label">{m.servers_xray_share_config_label()}</span>
		<div class="readonly-pill font-mono">
			{routeDescription(routes[0])} ({shownDomain(routes[0].domain)})
			· {isPrimaryFinite ? 'FiniteProto v2' : modeLabel(routes[0].mode)}
		</div>
	</div>
{/if}

<style>
	.endpoint-picker {
		display: flex;
		flex-direction: column;
		gap: 4px;
		margin-bottom: 4px;
	}

	.picker-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.picker-select {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 6px 10px;
		font-size: 12px;
		color: var(--color-text-primary);
		outline: none;
		cursor: pointer;
	}
	.picker-select:focus {
		border-color: var(--color-accent);
	}

	.route-pills {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
		gap: 6px;
	}

	.route-pill {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 7px 10px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		text-align: left;
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}
	.route-pill:hover {
		border-color: var(--color-accent);
		background: var(--color-bg-hover);
	}
	.route-pill.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.route-pill-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.route-pill-sub {
		font-size: 11px;
		color: var(--color-text-muted);
		/* Не break-all: тот рвёт любое слово («XHT TP»). anywhere переносит по
		   пробелам и режет только длинный домен/адрес, не влезающий в пилюлю. */
		overflow-wrap: anywhere;
	}

	.readonly-pill {
		padding: 6px 10px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-size: 12px;
		color: var(--color-text-secondary);
	}

	.font-mono {
		font-family: var(--font-mono);
	}
</style>
