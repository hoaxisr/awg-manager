<script lang="ts">
	import { m } from '$lib/i18n';
	import type { DnsUpstream } from '$lib/types';
	import { Badge } from '$lib/components/ui';
	import type { BadgeVariant } from '$lib/components/ui';
	interface Props { upstreams: DnsUpstream[]; }
	let { upstreams }: Props = $props();

	function encVariant(e: string): BadgeVariant {
		if (e === 'DoT') return 'success';
		if (e === 'DoH') return 'accent';
		return 'muted';
	}
	function scopeLabel(s: string): string {
		return s === 'all' ? m.diag_upstreams_scope_all() : `.${s}`;
	}
</script>

<table class="up-table">
	<thead>
		<tr><th>{m.diag_dns_col_server()}</th><th>{m.diag_upstreams_col_encryption()}</th><th>{m.diag_upstreams_col_host_sni()}</th><th>{m.diag_upstreams_col_domains()}</th></tr>
	</thead>
	<tbody>
		{#each upstreams as u}
			<tr>
				<td class="mono">{u.address}{#if u.port}<span class="faint">:{u.port}</span>{/if}</td>
				<td><Badge variant={encVariant(u.encryption)} size="sm">{u.encryption}</Badge></td>
				<td class="muted">{u.sni || '—'}</td>
				<td><span class="scope" class:scope-ru={u.scope !== 'all'}>{scopeLabel(u.scope)}</span></td>
			</tr>
		{/each}
	</tbody>
</table>

<div class="up-mobile-list" aria-label={m.diag_dns_upstreams()}>
	{#each upstreams as u}
		<section class="up-mobile-card">
			<div class="up-mobile-main">
				<div class="up-mobile-address mono">
					{u.address}{#if u.port}<span class="faint">:{u.port}</span>{/if}
				</div>
				<Badge variant={encVariant(u.encryption)} size="sm">{u.encryption}</Badge>
			</div>
			<div class="up-mobile-grid">
				<div class="up-mobile-field">
					<span class="up-mobile-label">{m.diag_upstreams_col_host_sni()}</span>
					<span class="up-mobile-value muted">{u.sni || '—'}</span>
				</div>
				<div class="up-mobile-field">
					<span class="up-mobile-label">{m.diag_upstreams_col_domains()}</span>
					<span class="scope" class:scope-ru={u.scope !== 'all'}>{scopeLabel(u.scope)}</span>
				</div>
			</div>
		</section>
	{/each}
</div>

<style>
	.up-table { width: 100%; border-collapse: collapse; }
	.up-table th { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--text-muted); text-align: left; padding: 0 10px 8px 0; }
	.up-table td { padding: 9px 10px 9px 0; border-top: 1px solid var(--border-soft, var(--border)); }
	.up-table tr:first-child td { border-top: none; }
	.up-mobile-list { display: none; }
	.mono { font-family: ui-monospace, monospace; font-size: 13px; }
	.faint { color: var(--text-muted); opacity: .7; }
	.muted { color: var(--text-muted); }
	.scope { font-size: 12px; color: var(--text-muted); }
	.scope-ru { color: var(--accent); font-weight: 600; }

	@media (max-width: 768px) {
		.up-table {
			display: none;
		}

		.up-mobile-list {
			display: grid;
			grid-template-columns: 1fr;
			gap: 0.625rem;
		}

		.up-mobile-card {
			min-width: 0;
			padding: 0.75rem;
			border: 1px solid var(--border-soft, var(--border));
			border-radius: var(--radius-sm);
			background: color-mix(in srgb, var(--color-bg-secondary, var(--bg-secondary)) 72%, transparent);
		}

		.up-mobile-main {
			display: flex;
			align-items: center;
			justify-content: space-between;
			gap: 0.75rem;
			min-width: 0;
			margin-bottom: 0.65rem;
		}

		.up-mobile-address {
			min-width: 0;
			overflow-wrap: anywhere;
			font-weight: 600;
			color: var(--text-primary);
		}

		.up-mobile-grid {
			display: grid;
			grid-template-columns: minmax(0, 1fr);
			gap: 0.5rem;
		}

		.up-mobile-field {
			display: grid;
			grid-template-columns: minmax(86px, auto) minmax(0, 1fr);
			align-items: baseline;
			gap: 0.75rem;
			min-width: 0;
		}

		.up-mobile-label {
			font-size: 10px;
			font-weight: 700;
			letter-spacing: 0.06em;
			text-transform: uppercase;
			color: var(--text-muted);
		}

		.up-mobile-value,
		.up-mobile-field .scope {
			min-width: 0;
			overflow-wrap: anywhere;
		}
	}
</style>
