<script lang="ts">
	import { m } from '$lib/i18n';
	import type { DnsStaticRecord } from '$lib/types';
	import { Badge } from '$lib/components/ui';
	interface Props { records: DnsStaticRecord[]; }
	let { records }: Props = $props();
	let open = $state(false);
</script>

<div class="static" class:open>
	<button type="button" class="head" onclick={() => (open = !open)}>
		<span class="chev">›</span>
		<span class="title">{m.diag_static_title()}</span>
		<Badge variant="muted" size="sm" mono>{records.length}</Badge>
	</button>
	{#if open}
		<table>
			<thead><tr><th>{m.diag_static_col_host()}</th><th>{m.diag_static_col_type()}</th><th>{m.diag_static_col_value()}</th><th class="num">Flag</th></tr></thead>
			<tbody>
				{#each records as r}
					<tr>
						<td class="mono">{r.host}</td>
						<td><Badge variant={r.type === 'AAAA' ? 'info' : 'success'} size="sm" mono>{r.type}</Badge></td>
						<td class="mono muted">{r.value}</td>
						<td class="num">{r.flag}</td>
					</tr>
				{/each}
			</tbody>
		</table>

		<div class="static-mobile-list" aria-label={m.diag_static_aria()}>
			{#each records as r}
				<section class="static-mobile-card">
					<div class="static-mobile-head">
						<span class="static-host mono">{r.host}</span>
						<Badge variant={r.type === 'AAAA' ? 'info' : 'success'} size="sm" mono>{r.type}</Badge>
					</div>
					<div class="static-mobile-field">
						<span class="static-mobile-label">{m.diag_static_col_value()}</span>
						<span class="static-value mono muted">{r.value}</span>
					</div>
					<div class="static-mobile-field">
						<span class="static-mobile-label">Flag</span>
						<span class="static-flag mono">{r.flag}</span>
					</div>
				</section>
			{/each}
		</div>
	{/if}
</div>

<style>
	.head { display: flex; align-items: center; gap: 8px; width: 100%; background: none; border: none; cursor: pointer; color: inherit; font: inherit; padding: 0; }
	.chev { color: var(--text-muted); transition: transform .15s; }
	.static.open .chev { transform: rotate(90deg); }
	.title { font-weight: 600; }
	table { width: 100%; border-collapse: collapse; margin-top: 12px; }
	th { font-size: 11px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: .04em; padding: 0 10px 8px 0; text-align: left; }
	td { padding: 8px 10px 8px 0; border-top: 1px solid var(--border-soft, var(--border)); font-size: 13px; }
	.static-mobile-list { display: none; }
	.mono { font-family: ui-monospace, monospace; }
	.muted { color: var(--text-muted); }
	.num { text-align: right; font-family: ui-monospace, monospace; }
	th.num { text-align: right; }

	@media (max-width: 768px) {
		table {
			display: none;
		}

		.static-mobile-list {
			display: grid;
			grid-template-columns: 1fr;
			gap: 0.625rem;
			margin-top: 12px;
		}

		.static-mobile-card {
			min-width: 0;
			padding: 0.75rem;
			border: 1px solid var(--border-soft, var(--border));
			border-radius: var(--radius-sm);
			background: color-mix(in srgb, var(--color-bg-secondary, var(--bg-secondary)) 72%, transparent);
		}

		.static-mobile-head {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: center;
			gap: 0.75rem;
			min-width: 0;
			margin-bottom: 0.65rem;
		}

		.static-host,
		.static-value {
			min-width: 0;
			overflow-wrap: anywhere;
			word-break: break-word;
		}

		.static-host {
			font-weight: 600;
			color: var(--text-primary);
		}

		.static-mobile-field {
			display: grid;
			grid-template-columns: minmax(64px, auto) minmax(0, 1fr);
			align-items: baseline;
			gap: 0.75rem;
			min-width: 0;
			margin-top: 0.45rem;
		}

		.static-mobile-label {
			font-size: 10px;
			font-weight: 700;
			letter-spacing: 0.06em;
			text-transform: uppercase;
			color: var(--text-muted);
		}

		.static-flag {
			color: var(--text-primary);
		}
	}
</style>
