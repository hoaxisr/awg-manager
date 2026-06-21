<script lang="ts">
	import type { MonitoringSnapshot, MonitoringTarget, MonitoringTunnel, MonitoringCell } from '$lib/types';
	import type { BadgeVariant } from '$lib/components/ui/Badge.svelte';
	import MatrixCell from './MatrixCell.svelte';
	import { Badge, LatencySparkline, VersionBadge } from '$lib/components/ui';
	import DefaultRouteBadge from '$lib/components/tunnels/DefaultRouteBadge.svelte';
	import { latencyTier } from '$lib/utils/latencyTier';
	import { latencyHistory } from '$lib/stores/singboxProxies';
	import { Settings } from 'lucide-svelte';

	interface Props {
		snapshot: MonitoringSnapshot;
		onCellClick: (target: MonitoringTarget, tunnel: MonitoringTunnel) => void;
		excludedTunnelIds?: Set<string>;
		onToggleTunnelExcluded?: (tunnelId: string, excluded: boolean, tunnelName: string) => void;
	}

	let {
		snapshot,
		onCellClick,
		excludedTunnelIds = new Set<string>(),
		onToggleTunnelExcluded = () => {},
	}: Props = $props();

	const sortedTunnels = $derived(
		[...snapshot.tunnels].sort((a, b) => a.name.localeCompare(b.name)),
	);

	function isSystem(t: MonitoringTunnel): boolean {
		return t.id.startsWith('sys-');
	}

	function isSingbox(t: MonitoringTunnel): boolean {
		return t.source === 'singbox';
	}

	// Managed AWG tunnels open the pingcheck drawer on the monitoring page.
	// System tunnels and sing-box t2sX are read-only — neither has NDMS-side
	// pingcheck (Keenetic owns the system case; sing-box uses Clash urltest).
	function tunnelHref(t: MonitoringTunnel): string {
		return `/monitoring?pingcheck=${encodeURIComponent(t.id)}`;
	}

	const cellByKey = $derived.by(() => {
		const m = new Map<string, MonitoringCell>();
		for (const c of snapshot.cells) {
			m.set(`${c.targetId}|${c.tunnelId}`, c);
		}
		return m;
	});

	function findCell(targetId: string, tunnelId: string): MonitoringCell | null {
		return cellByKey.get(`${targetId}|${tunnelId}`) ?? null;
	}

	const GOOGLE_CONNECTIVITY_HOST = 'connectivitycheck.gstatic.com';

	function isGoogleConnectivityTarget(target: MonitoringTarget): boolean {
		return target.host === GOOGLE_CONNECTIVITY_HOST || target.name === GOOGLE_CONNECTIVITY_HOST;
	}

	function mobileTargetName(target: MonitoringTarget): string {
		return isGoogleConnectivityTarget(target) ? 'Google' : target.name;
	}

	function mobileHostDomain(host: string): string {
		const parts = host.split('.');
		return parts.length > 2 ? parts.slice(-2).join('.') : host;
	}

	function mobileTargetHost(target: MonitoringTarget): string {
		return isGoogleConnectivityTarget(target) ? mobileHostDomain(target.host) : target.host;
	}

	function isExcluded(tunnelId: string): boolean {
		return excludedTunnelIds.has(tunnelId);
	}

	// Matrix exclusions are intentionally available for all row sources
	// (awg/system/singbox): controls visibility/probing in the monitoring
	// matrix only, not per-source pingcheck engines.
	function tunnelMatrixExcludeLabel(tunnel: MonitoringTunnel): string {
		const name = tunnel.name?.trim() || tunnel.id;
		return isExcluded(tunnel.id)
			? `Вернуть «${name}» в матрицу мониторинга`
			: `Исключить «${name}» из матрицы мониторинга`;
	}

	type TunnelBadge = {
		label: string;
		variant: BadgeVariant;
		mono?: boolean;
	};

	function normalizeProtoLabel(proto: string): string {
		switch (proto.toLowerCase()) {
			case 'vless':
				return 'VLESS';
			case 'hysteria2':
				return 'HY2';
			case 'shadowsocks':
				return 'SS';
			case 'trojan':
				return 'Trojan';
			case 'naive':
				return 'Naive';
			case 'mieru':
				return 'Mieru';
			default:
				return proto.toUpperCase();
		}
	}

	function tunnelTypeBadges(t: MonitoringTunnel): TunnelBadge[] {
		const out: TunnelBadge[] = [];
		if (t.source === 'singbox') {
			if (t.subscription) out.push({ label: 'подписка', variant: 'warning' });
			if (t.protocol) out.push({ label: normalizeProtoLabel(t.protocol), variant: 'accent', mono: true });
			if (t.security?.toLowerCase() === 'reality') out.push({ label: 'Reality', variant: 'warning' });
			else if (t.security?.toLowerCase() === 'tls') out.push({ label: 'TLS', variant: 'info' });
			if (t.transport) out.push({ label: t.transport.toUpperCase(), variant: 'muted', mono: true });
			if (out.length === 0) out.push({ label: 'SINGBOX', variant: 'muted', mono: true });
		}
		return out;
	}

	function resolvedAwgBackend(t: MonitoringTunnel): 'kernel' | 'nativewg' | '' {
		if (t.source !== 'awg') return '';
		if (t.backend === 'nativewg' || t.backend === 'kernel') return t.backend;
		if (t.ifaceName?.startsWith('nwg')) return 'nativewg';
		if (t.ifaceName?.startsWith('opkgtun') || t.ifaceName?.startsWith('awg')) return 'kernel';
		return '';
	}
</script>

{#if sortedTunnels.length === 0}
	<div class="empty">Нет работающих туннелей. Запустите хотя бы один туннель для отображения матрицы.</div>
{:else}
	<div class="wrap">
		<table class="matrix">
			<thead>
				<tr>
					<th class="th-target">Target</th>
					{#each sortedTunnels as t (t.id)}
						{@const typeBadges = tunnelTypeBadges(t)}
						{@const awgBackendValue = resolvedAwgBackend(t)}
						{@const showTypeRow = typeBadges.length > 0 || (t.source === 'awg' && (!!awgBackendValue || !!t.awgVersion))}
						<th class="th-tunnel">
							<div class="tunnel-head">
								<div class="tunnel-title-row">
									{#if isSystem(t)}
										<span class="tunnel-system" title="Системный туннель роутера — pingcheck управляется в системе">
											{t.name}
										</span>
									{:else if isSingbox(t)}
										<span class="tunnel-system" title="Sing-box туннель — мониторинг через Clash urltest, NDMS pingcheck не применяется">
											{t.name}
										</span>
									{:else}
										<a href={tunnelHref(t)} class="tunnel-link tunnel-name" title="Открыть настройки pingcheck">
											{t.name}
											<Settings size={14} style="opacity:0.7;flex-shrink:0" aria-hidden="true" />
										</a>
									{/if}
								</div>

								<div class="tunnel-toggle-row">
									{#if isExcluded(t.id)}
										<button
											type="button"
											class="exclude-btn exclude-btn-restore"
											onclick={() => onToggleTunnelExcluded(t.id, false, t.name)}
											title={tunnelMatrixExcludeLabel(t)}
											aria-label={tunnelMatrixExcludeLabel(t)}
										>
											<span class="sr-only">{tunnelMatrixExcludeLabel(t)}</span>
										</button>
									{:else}
										<button
											type="button"
											class="exclude-btn"
											onclick={() => onToggleTunnelExcluded(t.id, true, t.name)}
											title={tunnelMatrixExcludeLabel(t)}
											aria-label={tunnelMatrixExcludeLabel(t)}
										>
											<span class="sr-only">{tunnelMatrixExcludeLabel(t)}</span>
										</button>
									{/if}
								</div>
							</div>
							{#if t.source === 'awg' && !t.defaultRoute}
								<div class="tunnel-default-row">
									<DefaultRouteBadge defaultRoute={t.defaultRoute} />
								</div>
							{/if}
							{#if showTypeRow}
								<div class="tunnel-type-row">
									{#if t.source === 'awg'}
										{#if awgBackendValue}
											<VersionBadge kind="backend" value={awgBackendValue} />
										{/if}
										{#if t.awgVersion}
											<VersionBadge kind="awg" value={t.awgVersion} />
										{/if}
									{:else}
										{#each typeBadges as b, idx (`${t.id}-type-${idx}-${b.label}`)}
											<Badge variant={b.variant} size="sm" mono={b.mono ?? false}>{b.label}</Badge>
										{/each}
									{/if}
								</div>
							{/if}
							{#if t.source === 'singbox' && t.clashDelay && t.clashDelay > 0}
								<div class="tunnel-badge-row">
									<Badge
										variant={latencyTier(t.clashDelay)}
										size="sm"
										mono
										title={`Источник: urltest группа "${t.urltestGroup ?? ''}"`}
									>
										<span class="clash-num">clash: <span class="clash-val">{t.clashDelay}</span>ms</span>
										<LatencySparkline
											history={$latencyHistory.get(t.singboxTag ?? '') ?? []}
											width={36}
											height={10}
										/>
									</Badge>
								</div>
							{/if}
						</th>
					{/each}
				</tr>
			</thead>
			<tbody>
				{#each snapshot.targets as target (target.id)}
					<tr>
						<th class="td-target" scope="row">
							{#if isGoogleConnectivityTarget(target)}
								<div class="target-desktop">
									<span class="target-name">{target.name}</span>
									<span class="target-host">{target.host}</span>
								</div>

								<div class="target-mobile-google" title={target.host}>
									<div class="target-name">{mobileTargetName(target)}</div>
									<div class="target-host">{mobileTargetHost(target)}</div>
								</div>
							{:else}
								<span class="target-name">{target.name}</span>
								<span class="target-host">{target.host}</span>
							{/if}
						</th>
						{#each sortedTunnels as tunnel (tunnel.id)}
							{@const cell = findCell(target.id, tunnel.id)}
							<td class="td-cell">
								{#if cell}
									<MatrixCell
										latencyMs={cell.latencyMs}
										ok={cell.ok}
										activeForRestart={cell.activeForRestart}
										onClick={() => onCellClick(target, tunnel)}
										ariaLabel="{target.name} × {tunnel.name}"
									/>
								{:else}
									<MatrixCell latencyMs={null} ok={false} activeForRestart={false} ariaLabel="no data" />
								{/if}
							</td>
						{/each}
					</tr>
				{/each}
			</tbody>
		</table>
		<div class="mobile-cards" aria-label="Матрица мониторинга">
			{#each sortedTunnels as tunnel (tunnel.id)}
				{@const typeBadges = tunnelTypeBadges(tunnel)}
				{@const awgBackendValue = resolvedAwgBackend(tunnel)}
				{@const showTypeRow = typeBadges.length > 0 || (tunnel.source === 'awg' && (!!awgBackendValue || !!tunnel.awgVersion))}
				{@const showMobileBadgeRow =
					showTypeRow ||
					(tunnel.source === 'awg' && !tunnel.defaultRoute) ||
					(tunnel.source === 'singbox' && !!tunnel.clashDelay && tunnel.clashDelay > 0)}
				<section class="mobile-tunnel-card" aria-label={`Мониторинг ${tunnel.name}`}>
					<header class="mobile-tunnel-head">
						<div class="mobile-tunnel-main">
							<div class="mobile-tunnel-title-row">
								{#if isSystem(tunnel)}
									<span class="tunnel-system" title="Системный туннель роутера — pingcheck управляется в системе">
										{tunnel.name}
									</span>
								{:else if isSingbox(tunnel)}
									<span class="tunnel-system" title="Sing-box туннель — мониторинг через Clash urltest, NDMS pingcheck не применяется">
										{tunnel.name}
									</span>
								{:else}
									<a href={tunnelHref(tunnel)} class="tunnel-link tunnel-name" title="Открыть настройки pingcheck">
										{tunnel.name}
										<Settings size={14} style="opacity:0.7;flex-shrink:0" aria-hidden="true" />
									</a>
								{/if}
							</div>

							{#if showMobileBadgeRow}
								<div class="mobile-tunnel-type-row">
									{#if tunnel.source === 'awg'}
										{#if awgBackendValue}<VersionBadge kind="backend" value={awgBackendValue} />{/if}
										{#if tunnel.awgVersion}<VersionBadge kind="awg" value={tunnel.awgVersion} />{/if}
										<DefaultRouteBadge defaultRoute={tunnel.defaultRoute} />
									{:else}
										{#each typeBadges as b, idx (`${tunnel.id}-mobile-type-${idx}-${b.label}`)}
											<Badge variant={b.variant} size="sm" mono={b.mono ?? false}>{b.label}</Badge>
										{/each}
										{#if tunnel.source === 'singbox' && tunnel.clashDelay && tunnel.clashDelay > 0}
											<Badge
												variant={latencyTier(tunnel.clashDelay)}
												size="sm"
												mono
												title={`Источник: urltest группа "${tunnel.urltestGroup ?? ''}"`}
											>
												<span class="clash-num">clash: <span class="clash-val">{tunnel.clashDelay}</span>ms</span>
												<LatencySparkline
													history={$latencyHistory.get(tunnel.singboxTag ?? '') ?? []}
													width={36}
													height={10}
												/>
											</Badge>
										{/if}
									{/if}
								</div>
							{/if}
						</div>

						<button
							type="button"
							class="exclude-btn"
							class:exclude-btn-restore={isExcluded(tunnel.id)}
							onclick={() => onToggleTunnelExcluded(tunnel.id, !isExcluded(tunnel.id), tunnel.name)}
							title={tunnelMatrixExcludeLabel(tunnel)}
							aria-label={tunnelMatrixExcludeLabel(tunnel)}
						>
							<span class="sr-only">{tunnelMatrixExcludeLabel(tunnel)}</span>
						</button>
					</header>

					<div class="mobile-target-list">
						{#each snapshot.targets as target (target.id)}
							{@const cell = findCell(target.id, tunnel.id)}
							<div class="mobile-target-row">
								<div class="mobile-target-info">
									<span class="target-name">{mobileTargetName(target)}</span>
									<span class="target-host">{mobileTargetHost(target)}</span>
								</div>
								{#if cell}
									<MatrixCell
										latencyMs={cell.latencyMs}
										ok={cell.ok}
										activeForRestart={cell.activeForRestart}
										onClick={() => onCellClick(target, tunnel)}
										ariaLabel={`${target.name} × ${tunnel.name}`}
									/>
								{:else}
									<MatrixCell latencyMs={null} ok={false} activeForRestart={false} ariaLabel="no data" />
								{/if}
							</div>
						{/each}
					</div>
				</section>
			{/each}
		</div>


		<div class="legend">
			<span class="legend-item"><span class="swatch tone-good"></span>&lt;100ms</span>
			<span class="legend-item"><span class="swatch tone-warn"></span>100-250ms</span>
			<span class="legend-item"><span class="swatch tone-bad"></span>&gt;250ms</span>
			<span class="legend-item"><span class="swatch tone-failed"></span>failed</span>
			<span class="legend-item">★ — активный pingcheck target</span>
			<span class="legend-item">Клик на имя туннеля — настройки pingcheck</span>
		</div>
	</div>
{/if}

<style>
	.wrap {
		overflow-x: auto;
	}

	.sr-only {
		position: absolute;
		width: 1px;
		height: 1px;
		padding: 0;
		margin: -1px;
		overflow: hidden;
		clip: rect(0, 0, 0, 0);
		clip-path: inset(50%);
		white-space: nowrap;
		border: 0;
	}

	.mobile-cards {
		display: none;
	}

	.mobile-tunnel-card {
		display: flex;
		flex-direction: column;
		min-width: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		overflow: hidden;
	}

	.mobile-tunnel-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
	}

	.mobile-tunnel-main {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		min-width: 0;
	}

	.mobile-tunnel-title-row,
	.mobile-tunnel-type-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.35rem;
		min-width: 0;
	}

	.mobile-target-list {
		display: flex;
		flex-direction: column;
	}

	.mobile-target-row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.75rem;
		padding: 0.625rem 0.75rem;
		border-top: 1px solid color-mix(in srgb, var(--color-border) 60%, transparent);
	}

	.mobile-target-row:first-child {
		border-top: none;
	}

	.mobile-target-info {
		min-width: 0;
	}

	.mobile-target-row :global(.cell) {
		width: 92px;
		min-width: 92px;
		height: 30px;
	}

	@media (max-width: 768px) {
		.wrap {
			overflow-x: visible;
		}

		.matrix {
			display: none;
		}

		.mobile-cards {
			display: grid;
			grid-template-columns: 1fr;
			gap: 0.75rem;
		}

		.mobile-tunnel-card .tunnel-link,
		.mobile-tunnel-card .tunnel-system {
			font-size: 14px;
			font-weight: 600;
			line-height: 1.3;
			color: var(--color-text-primary);
			max-width: 100%;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

	}

	.clash-num {
		font-variant-numeric: tabular-nums;
	}

	.clash-val {
		display: inline-block;
		min-width: 3ch;
		text-align: right;
	}

	.matrix {
		border-collapse: separate;
		border-spacing: 0.375rem;
		width: 100%;
	}

	.th-target,
	.th-tunnel {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		padding: 0.4375rem 0.5rem;
		text-align: left;
		background: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
		position: sticky;
		top: 0;
	}

	.th-tunnel {
		min-width: 100px;
		text-align: center;
		z-index: 1;
	}

	.tunnel-head {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		flex-wrap: wrap;
		justify-content: flex-end;
	}

	.tunnel-title-row {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
	}

	.tunnel-toggle-row {
		display: flex;
		justify-content: flex-end;
	}

	.tunnel-badge-row {
		display: flex;
		justify-content: center;
		width: 100%;
		margin-top: 6px;
	}

	.th-tunnel > .tunnel-default-row {
		display: flex;
		justify-content: center;
		width: 100%;
		margin-top: 0.15rem;
	}

	.tunnel-type-row {
		display: flex;
		justify-content: center;
		flex-wrap: wrap;
		gap: 0.25rem;
		width: 100%;
		margin-top: 4px;
	}

	@media (max-width: 768px) {
		.th-tunnel {
			padding: 0.5rem 0.5rem 0.625rem;
			text-align: center;
			vertical-align: middle;
		}

		.th-tunnel > .tunnel-head {
			display: flex;
			flex-direction: column;
			align-items: center;
			justify-content: center;
			gap: 6px;
			width: 100%;
			min-width: 0;
			margin: 0 auto;
		}

		.th-tunnel > .tunnel-head > .tunnel-title-row,
		.th-tunnel > .tunnel-head > .tunnel-toggle-row {
			display: flex;
			justify-content: center;
			align-items: center;
			width: 100%;
			min-width: 0;
			margin: 0 auto;
			text-align: center;
		}

		.th-tunnel > .tunnel-head > .tunnel-title-row > .tunnel-link,
		.th-tunnel > .tunnel-head > .tunnel-title-row > .tunnel-system {
			display: inline-flex;
			align-items: center;
			justify-content: center;
			max-width: 100%;
			min-width: 0;
			margin: 0 auto;
			text-align: center;
		}

		.th-tunnel > .tunnel-head > .tunnel-title-row > .tunnel-name,
		.th-tunnel > .tunnel-head > .tunnel-title-row > .tunnel-link {
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.exclude-btn {
			margin: 0 auto;
		}

		.th-tunnel > .tunnel-badge-row {
			display: flex;
			justify-content: center;
			align-items: center;
			width: 100%;
			min-width: 0;
			margin-top: 6px;
			text-align: center;
		}

		.th-tunnel > .tunnel-type-row {
			display: flex;
			justify-content: center;
			align-items: center;
			flex-wrap: wrap;
			gap: 0.25rem;
			width: 100%;
			min-width: 0;
			margin-top: 4px;
			text-align: center;
		}
	}

	.tunnel-link {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		color: inherit;
		text-decoration: none;
		padding: 0.125rem 0.375rem;
		border-radius: var(--radius-sm);
		transition: color var(--t-fast) ease, background var(--t-fast) ease;
	}
	.tunnel-link:hover {
		color: var(--color-accent);
		background: var(--color-bg-hover);
	}

	.exclude-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		height: 22px;
		padding: 0 0.5rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.02em;
		cursor: pointer;
		transition:
			background var(--t-fast) ease,
			color var(--t-fast) ease,
			border-color var(--t-fast) ease,
			box-shadow var(--t-fast) ease;
		white-space: nowrap;
	}
	.exclude-btn:hover {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}
	.exclude-btn:focus-visible {
		outline: none;
		box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-accent) 45%, transparent);
	}
	.exclude-btn-restore {
		border-color: color-mix(in srgb, var(--color-error) 45%, var(--color-border));
		color: var(--color-error);
	}
	.exclude-btn-restore:hover {
		background: color-mix(in srgb, var(--color-error) 12%, var(--color-bg-hover));
		color: var(--color-error);
	}

	.tunnel-system {
		display: inline-block;
		padding: 0.125rem 0.375rem;
		color: var(--color-text-muted);
		cursor: help;
	}

	.th-target {
		left: 0;
		z-index: 2;
	}

	.td-target {
		padding: 0.375rem 0.5rem;
		text-align: left;
		font-size: 12px;
		background: var(--color-bg-secondary);
		position: sticky;
		left: 0;
		z-index: 1;
		min-width: 160px;
	}

	.target-name {
		display: block;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.target-host {
		display: block;
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.target-desktop {
		display: block;
	}

	.target-mobile-google {
		display: none;
	}

	.target-mobile-google .target-host {
		overflow-wrap: anywhere;
		line-height: 1.25;
	}

	@media (max-width: 768px) {
		.target-mobile-google {
			display: block;
		}

		.target-desktop {
			display: none;
		}

		.target-host {
			overflow-wrap: anywhere;
			line-height: 1.25;
		}
	}

	.td-cell {
		padding: 0.125rem;
		text-align: center;
	}

	.empty {
		padding: 3rem 1rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 14px;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
	}

	.legend {
		display: flex;
		gap: 1rem;
		flex-wrap: wrap;
		margin-top: 0.75rem;
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.legend-item {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
	}

	.swatch {
		display: inline-block;
		width: 12px;
		height: 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
	}

	.swatch.tone-good { background: color-mix(in srgb, var(--color-success) 50%, transparent); }
	.swatch.tone-warn { background: color-mix(in srgb, var(--color-warning) 50%, transparent); }
	.swatch.tone-bad { background: color-mix(in srgb, var(--color-error) 50%, transparent); }
	.swatch.tone-failed { background: var(--color-muted-tint); }

	@media (max-width: 768px) {
		.mobile-tunnel-head {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: start;
			gap: 0.5rem;
		}

		.mobile-tunnel-main {
			min-width: 0;
		}

		.mobile-tunnel-head > .exclude-btn {
			display: inline-flex;
			align-items: center;
			justify-content: center;
			justify-self: end;
			align-self: start;
			gap: 0.35rem;
			width: auto;
			max-width: max-content;
			height: 24px;
			padding: 0 0.55rem;
			border-radius: 999px;
			border: 1px solid color-mix(in srgb, var(--color-error) 35%, var(--color-border));
			background: color-mix(in srgb, var(--color-error) 10%, transparent);
			color: var(--color-text-primary);
			font-size: 11px;
			font-weight: 600;
			line-height: 1;
			letter-spacing: 0;
			white-space: nowrap;
			box-shadow: none;
		}

		.mobile-tunnel-head > .exclude-btn::before {
			content: '';
			width: 14px;
			height: 14px;
			flex-shrink: 0;
			background: currentColor;
			-webkit-mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='black' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94'/%3E%3Cpath d='M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19'/%3E%3Cpath d='M14.12 14.12a3 3 0 0 1-4.24-4.24'/%3E%3Cline x1='1' y1='1' x2='23' y2='23'/%3E%3C/svg%3E") center / contain no-repeat;
			mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='black' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94'/%3E%3Cpath d='M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19'/%3E%3Cpath d='M14.12 14.12a3 3 0 0 1-4.24-4.24'/%3E%3Cline x1='1' y1='1' x2='23' y2='23'/%3E%3C/svg%3E") center / contain no-repeat;
		}

		.mobile-tunnel-head > .exclude-btn.exclude-btn-restore::before {
			-webkit-mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='black' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z'/%3E%3Ccircle cx='12' cy='12' r='3'/%3E%3C/svg%3E") center / contain no-repeat;
			mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='black' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z'/%3E%3Ccircle cx='12' cy='12' r='3'/%3E%3C/svg%3E") center / contain no-repeat;
		}

		.mobile-tunnel-head > .exclude-btn:hover,
		.mobile-tunnel-head > .exclude-btn.exclude-btn-restore:hover {
			background: color-mix(in srgb, var(--color-error) 16%, transparent);
			border-color: color-mix(in srgb, var(--color-error) 50%, var(--color-border));
			color: var(--color-text-primary);
		}

		.mobile-tunnel-head > .exclude-btn:focus-visible {
			outline: 2px solid color-mix(in srgb, var(--color-error) 45%, transparent);
			outline-offset: 2px;
		}
	}


	/* Desktop header uses the same visual language as mobile/excluded chips,
	   but the action owns a full row inside the tunnel header cell. */
	.th-tunnel > .tunnel-head {
		display: flex;
		flex-direction: column;
		align-items: stretch;
		justify-content: center;
		gap: 0.35rem;
		width: 100%;
		min-width: 0;
	}

	.th-tunnel > .tunnel-head > .tunnel-title-row,
	.th-tunnel > .tunnel-head > .tunnel-toggle-row {
		width: 100%;
		min-width: 0;
	}

	.th-tunnel > .tunnel-head > .tunnel-title-row {
		justify-content: center;
		text-align: center;
	}

	.th-tunnel > .tunnel-head > .tunnel-toggle-row {
		justify-content: stretch;
	}

	.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn {
		width: 100%;
	}

	.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn,
	.mobile-tunnel-head > .exclude-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 0.35rem;
		height: 24px;
		padding: 0 0.55rem;
		border-radius: 999px;
		border: 1px solid color-mix(in srgb, var(--color-error) 35%, var(--color-border));
		background: color-mix(in srgb, var(--color-error) 10%, transparent);
		color: var(--color-text-primary);
		font-size: 11px;
		font-weight: 600;
		line-height: 1;
		letter-spacing: 0;
		white-space: nowrap;
		box-shadow: none;
	}

	.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn::before,
	.mobile-tunnel-head > .exclude-btn::before {
		content: '';
		width: 14px;
		height: 14px;
		flex-shrink: 0;
		background: currentColor;
		-webkit-mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='black' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94'/%3E%3Cpath d='M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19'/%3E%3Cpath d='M14.12 14.12a3 3 0 0 1-4.24-4.24'/%3E%3Cline x1='1' y1='1' x2='23' y2='23'/%3E%3C/svg%3E") center / contain no-repeat;
		mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='black' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94'/%3E%3Cpath d='M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19'/%3E%3Cpath d='M14.12 14.12a3 3 0 0 1-4.24-4.24'/%3E%3Cline x1='1' y1='1' x2='23' y2='23'/%3E%3C/svg%3E") center / contain no-repeat;
	}

	.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn.exclude-btn-restore::before,
	.mobile-tunnel-head > .exclude-btn.exclude-btn-restore::before {
		-webkit-mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='black' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z'/%3E%3Ccircle cx='12' cy='12' r='3'/%3E%3C/svg%3E") center / contain no-repeat;
		mask: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='black' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z'/%3E%3Ccircle cx='12' cy='12' r='3'/%3E%3C/svg%3E") center / contain no-repeat;
	}

	.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn:hover,
	.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn.exclude-btn-restore:hover,
	.mobile-tunnel-head > .exclude-btn:hover,
	.mobile-tunnel-head > .exclude-btn.exclude-btn-restore:hover {
		background: color-mix(in srgb, var(--color-error) 16%, transparent);
		border-color: color-mix(in srgb, var(--color-error) 50%, var(--color-border));
		color: var(--color-text-primary);
	}

	.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn:focus-visible,
	.mobile-tunnel-head > .exclude-btn:focus-visible {
		outline: 2px solid color-mix(in srgb, var(--color-error) 45%, transparent);
		outline-offset: 2px;
	}

	/* Desktop monitoring: keep the visibility toggle inline with the tunnel name.
	   The matrix eye is a lightweight icon action, not a red exclusion chip. */
	@media (min-width: 769px) {
		.th-tunnel > .tunnel-head {
			display: inline-flex;
			flex-direction: row;
			align-items: center;
			justify-content: center;
			gap: 0.25rem;
			width: 100%;
			min-width: 0;
		}

		.th-tunnel > .tunnel-head > .tunnel-title-row,
		.th-tunnel > .tunnel-head > .tunnel-toggle-row {
			width: auto;
			min-width: 0;
		}

		.th-tunnel > .tunnel-head > .tunnel-title-row {
			flex: 0 1 auto;
			justify-content: center;
			text-align: center;
		}

		.th-tunnel > .tunnel-head > .tunnel-toggle-row {
			flex: 0 0 auto;
			justify-content: center;
		}

		.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn,
		.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn.exclude-btn-restore {
			width: 22px;
			height: 22px;
			padding: 0;
			border: 0;
			border-radius: var(--radius-sm);
			background: transparent;
			color: var(--color-text-muted);
			font-size: 0;
			box-shadow: none;
		}

		.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn::before {
			width: 14px;
			height: 14px;
		}

		.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn:hover,
		.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn.exclude-btn-restore:hover {
			background: var(--color-bg-hover);
			border-color: transparent;
			color: var(--color-text-primary);
		}

		.th-tunnel > .tunnel-head > .tunnel-toggle-row > .exclude-btn:focus-visible {
			outline: 2px solid color-mix(in srgb, var(--color-accent) 45%, transparent);
			outline-offset: 2px;
			box-shadow: none;
		}
	}

</style>
