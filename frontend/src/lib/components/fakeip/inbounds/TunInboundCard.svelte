<!--
  Карточка tun-in (READ-ONLY) по мокапу page-inbounds-v2 (.ic2.core).
  Единственный вход движка fakeip-tun; управляется движком (правки нет).
  Значения — факты из бэкенда (interface/address/стек·MTU/DNS): iface+address+DNS
  из status (fakeipIface/fakeipTunAddr/fakeipDns), стек·MTU из settings.
-->
<script lang="ts">
	import { Lock } from 'lucide-svelte';

	interface Props {
		/** Активный fakeip tun-интерфейс (status.fakeipIface), e.g. «opkgtun10». */
		iface?: string;
		/** Адрес tun-шлюза (status.fakeipTunAddr), e.g. «172.18.0.1». */
		address?: string;
		/** DNS клиентам для ручной настройки (status.fakeipDns). */
		tunDns?: string;
		fakeipStack?: 'gvisor' | 'system';
		fakeipMtu?: number;
		/** Движок запущен → статус-точка достоверна (success), иначе muted. */
		live?: boolean;
	}
	let {
		iface,
		address,
		tunDns,
		fakeipStack = 'gvisor',
		fakeipMtu,
		live = false,
	}: Props = $props();

	const stackMtuLabel = $derived(
		fakeipMtu ? `${fakeipStack} · ${fakeipMtu}` : fakeipStack,
	);
	const dnsLabel = $derived(tunDns ? `${tunDns} (hijack)` : '—');
</script>

<article class="ic2 core">
	<div class="top">
		<span class="type tun">tun</span>
		<span class="nm">tun-in</span>
		<span class="badge">ядро fakeip</span>
		<span class="dot" data-tone={live ? 'success' : 'muted'} aria-hidden="true"></span>
	</div>

	<div class="rows">
		<div class="r"><span class="k">interface</span><span class="v y">{iface || '—'}</span></div>
		<div class="r"><span class="k">address</span><span class="v mono">{address || '—'}</span></div>
		<div class="r"><span class="k">стек · MTU</span><span class="v">{stackMtuLabel}</span></div>
		<div class="r"><span class="k">DNS клиентам</span><span class="v">{dnsLabel}</span></div>
	</div>

	<div class="foot">
		<span class="locked">
			<Lock size={12} aria-hidden="true" /> управляется движком
		</span>
	</div>
</article>

<style>
	.ic2 {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius, 12px);
		padding: 1rem;
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.ic2.core {
		border-color: var(--color-accent-border, var(--color-accent));
	}

	.top {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.625rem;
	}

	.type {
		font-size: 0.625rem;
		border-radius: 5px;
		padding: 0.1rem 0.4rem;
		border: 1px solid var(--border);
		color: var(--text-muted);
		font-family: var(--font-mono);
	}

	.type.tun {
		color: var(--color-accent);
		border-color: var(--color-accent-border, var(--color-accent));
	}

	.nm {
		color: var(--text-primary);
		font-size: 0.9375rem;
		font-weight: 700;
	}

	.badge {
		font-size: 0.5625rem;
		color: var(--color-accent-contrast, #0a0a0a);
		background: var(--color-accent);
		border-radius: 4px;
		padding: 0.05rem 0.4rem;
		font-weight: 700;
	}

	.dot {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		margin-left: auto;
		background: var(--text-muted);
		flex-shrink: 0;
	}

	.dot[data-tone='success'] {
		background: var(--color-success, #22c55e);
	}

	.rows {
		display: flex;
		flex-direction: column;
	}

	.r {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 0.75rem;
		padding: 0.45rem 0;
		border-top: 1px solid var(--border);
		font-size: 0.8125rem;
	}

	.r:first-child {
		border-top: none;
	}

	.k {
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.v {
		color: var(--text-primary);
		text-align: right;
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.v.y {
		color: var(--color-accent);
	}

	.v.mono {
		font-family: var(--font-mono);
	}

	.foot {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		margin-top: 0.75rem;
		border-top: 1px solid var(--border);
		padding-top: 0.7rem;
	}

	.locked {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.6875rem;
		color: var(--text-muted);
		border: 1px dashed var(--border);
		border-radius: 6px;
		padding: 0.25rem 0.45rem;
		cursor: not-allowed;
	}
</style>
