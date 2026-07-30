<script lang="ts">
	import type { FreeTurnLinkPayload } from '$lib/types';

	interface Props {
		payload: FreeTurnLinkPayload | null;
		peer?: string;
	}

	let { payload, peer = '' }: Props = $props();

	const rows = $derived.by(() => {
		if (!payload) return [];
		const out: { k: string; v: string }[] = [];
		if (payload.peer || peer) out.push({ k: 'peer', v: payload.peer || peer });
		if (payload.provider) out.push({ k: 'provider', v: payload.provider });
		if (payload.transport) out.push({ k: 'transport', v: payload.transport });
		if (payload.mode) out.push({ k: 'mode', v: payload.mode });
		if (payload.obf) out.push({ k: 'obf', v: payload.obf });
		if (payload.key) out.push({ k: 'key', v: `${payload.key.slice(0, 8)}…` });
		if (payload.cid) out.push({ k: 'client ID', v: payload.cid });
		if (payload.mtu) out.push({ k: 'MTU', v: String(payload.mtu) });
		if (payload.n) out.push({ k: 'потоков', v: String(payload.n) });
		if (payload.spc) out.push({ k: 'на кред', v: String(payload.spc) });
		if (payload.listen) out.push({ k: 'listen', v: payload.listen });
		if (payload.name) out.push({ k: 'имя', v: payload.name });
		if (payload.wg) out.push({ k: 'WireGuard', v: 'конфиг в ссылке' });
		return out;
	});
</script>

{#if payload && rows.length > 0}
	<div class="ft-link-params">
		<div class="section-label">Параметры в ссылке</div>
		<dl class="ft-link-params-grid">
			{#each rows as row (row.k)}
				<dt>{row.k}</dt>
				<dd>{row.v}</dd>
			{/each}
		</dl>
	</div>
{/if}

<style>
	.ft-link-params {
		margin-top: 0.75rem;
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
	}

	.ft-link-params-grid {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 0.25rem 0.75rem;
		margin: 0.375rem 0 0;
		font-size: 0.75rem;
	}

	dt {
		color: var(--color-text-secondary);
		font-family: var(--font-mono);
	}

	dd {
		margin: 0;
		font-family: var(--font-mono);
		word-break: break-all;
	}
</style>
