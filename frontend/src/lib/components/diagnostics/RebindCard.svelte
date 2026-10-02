<script lang="ts">
	import { m } from '$lib/i18n';
	import type { DnsRebind } from '$lib/types';
	import { Badge, StatusDot } from '$lib/components/ui';
	interface Props { rebind: DnsRebind; }
	let { rebind }: Props = $props();
</script>

<div class="rebind">
	<div class="head">
		<span class="label">{m.diag_rebind_title()}</span>
		<span class="status">
			<StatusDot variant={rebind.enabled ? 'success' : 'muted'} size="sm" />
			{rebind.enabled ? m.diag_rebind_on() : m.diag_rebind_off()}
		</span>
	</div>
	<div class="kv">
		<div>
			<div class="k">{m.diag_rebind_networks()}</div>
			<div class="tags">
				{#each rebind.nets as n}<Badge variant="muted" size="sm" mono>{n}</Badge>{:else}<span class="muted">—</span>{/each}
			</div>
		</div>
		<div>
			<div class="k">{m.diag_rebind_exceptions()}</div>
			<div class="tags">
				{#each rebind.excludes as e}<Badge variant="muted" size="sm" mono>{e}</Badge>{:else}<span class="muted">—</span>{/each}
			</div>
		</div>
	</div>
</div>

<style>
	.head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
	.label { font-size: 11px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; color: var(--text-muted); }
	.status { display: inline-flex; align-items: center; gap: 6px; font-weight: 600; font-size: 13px; }
	.kv { display: flex; gap: 28px; flex-wrap: wrap; }
	.kv .k { font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: .04em; color: var(--text-muted); margin-bottom: 6px; }
	.tags { display: flex; flex-wrap: wrap; gap: 6px; }
	.muted { color: var(--text-muted); }
</style>
