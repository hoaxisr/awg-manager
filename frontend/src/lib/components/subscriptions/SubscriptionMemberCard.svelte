<script lang="ts">
	import { onMount } from 'svelte';
	import type { SubscriptionMember } from '$lib/types';
	import { singboxDelayHistory, triggerDelayCheck } from '$lib/stores/singbox';
	const SOFT_CARD_LATENCY_EVENT = 'awgm:soft-card-latency-refresh';

	interface Props {
		member: SubscriptionMember;
		active: boolean;
		switching: boolean;
		disabled: boolean;
		onclick: () => void;
	}
	let { member, active, switching, disabled, onclick }: Props = $props();

	const history = $derived($singboxDelayHistory.get(member.tag) ?? []);
	const latest = $derived(history.length > 0 ? history[history.length - 1] : -1);

	let testing = $state(false);
	let showServer = $state(false);
	let lastSoftWarmupAt = 0;
	async function runTest(e: MouseEvent | KeyboardEvent): Promise<void> {
		e.stopPropagation(); // don't trigger card-as-radio click
		if (testing) return;
		testing = true;
		try {
			await triggerDelayCheck(member.tag);
		} finally {
			testing = false;
		}
	}
	async function runSoftDelayWarmup(): Promise<void> {
		if (testing) return;
		const now = Date.now();
		if (now - lastSoftWarmupAt < 15_000) return;
		lastSoftWarmupAt = now;
		await runTest(new MouseEvent('click'));
	}
	function onTestKeydown(e: KeyboardEvent): void {
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			runTest(e);
		}
	}

	const DELAY_OK = 200;
	const DELAY_SLOW = 500;

	function delayStateOf(d: number): 'ok' | 'slow' | 'fail' | 'unknown' {
		if (d < 0) return 'unknown';
		if (d === 0) return 'fail';
		if (d < DELAY_OK) return 'ok';
		if (d < DELAY_SLOW) return 'slow';
		return 'fail';
	}
	const delayState = $derived(delayStateOf(latest));
	const delayText = $derived.by(() => {
		if (delayState === 'unknown') return '—';
		if (delayState === 'fail') return 'timeout';
		return `${latest}ms`;
	});

	const protocolLabel = $derived.by(() => {
		switch (member.protocol) {
			case 'vless': return 'VLESS';
			case 'trojan': return 'Trojan';
			case 'shadowsocks': return 'Shadowsocks';
			case 'hysteria2': return 'Hysteria2';
			case 'naive': return 'Naive';
			default: return member.protocol;
		}
	});
	const memberSNI = $derived.by(() => {
		const m = member as unknown as Record<string, unknown>;
		const candidates = [
			m.sni,
			m.serverName,
			m.server_name,
			m.tlsServerName,
			m.tls_server_name,
		];
		for (const v of candidates) {
			if (typeof v === 'string' && v.trim() !== '') return v.trim();
		}
		return '';
	});

	const titleText = $derived.by(() => {
		if (member.label && member.label.trim() !== '') return member.label;
		return showServer ? member.server : '●●●●●●●●';
	});

	onMount(() => {
		const onSoftRefresh = (event: Event) => {
			const detail = (event as CustomEvent<{ target?: string }>).detail;
			if (detail?.target !== 'subscription-members') return;
			void runSoftDelayWarmup();
		};
		window.addEventListener(SOFT_CARD_LATENCY_EVENT, onSoftRefresh as EventListener);
		return () => window.removeEventListener(SOFT_CARD_LATENCY_EVENT, onSoftRefresh as EventListener);
	});
</script>

<button
	type="button"
	class="card"
	class:active
	class:switching
	{disabled}
	onclick={onclick}
	aria-pressed={active}
>
	<div class="header">
		<span class="led" class:on={active} aria-hidden="true"></span>
		<span class="title" title={member.tag}>{titleText}</span>
		<span
			class="icon-btn"
			role="button"
			tabindex="0"
			aria-label={showServer ? 'Скрыть сервер' : 'Показать сервер'}
			onclick={(e) => {
				e.stopPropagation();
				showServer = !showServer;
			}}
			onkeydown={(e) => {
				if (e.key === 'Enter' || e.key === ' ') {
					e.preventDefault();
					e.stopPropagation();
					showServer = !showServer;
				}
			}}
		>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/>
				<circle cx="12" cy="12" r="3"/>
			</svg>
		</span>
		<span class="port mono">:{member.port}</span>
	</div>
	<div class="badges">
		<span class="badge proto">{protocolLabel}</span>
		{#if member.transport && member.transport !== 'tcp'}
			<span class="badge transport">{member.transport.toUpperCase()}</span>
		{/if}
		{#if member.security === 'reality'}
			<span class="badge reality">Reality</span>
		{:else if member.security === 'tls'}
			<span class="badge tls">TLS</span>
		{/if}
	</div>
	{#if memberSNI}
		<div class="sni-under-title mono" title={memberSNI}>
			SNI:
			{#if showServer}
				{memberSNI}
			{:else}
				●●●●●●●●
			{/if}
		</div>
	{/if}
	{#if member.label}
		<div class="server-line mono" title={member.tag}>
			{#if showServer}
				{member.server}
			{:else}
				●●●●●●●●
			{/if}
			:{member.port}
		</div>
	{/if}
	<div class="delay-row">
		<span
			role="button"
			tabindex="0"
			class="delay-btn {delayState}"
			class:is-disabled={testing}
			aria-disabled={testing}
			onclick={runTest}
			onkeydown={onTestKeydown}
			title="Проверить delay"
		>
			{testing ? '...' : delayText}
		</span>
		<div class="spark {delayState}">
			{#if history.length === 0}
				{#each Array(6) as _, i (i)}<div class="bar empty"></div>{/each}
			{:else}
				{@const max = Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100)}
				{#each history as d, i (i)}
					<div class="bar" style="height: {Math.max((d <= 0 ? max : d) / max, 0.1) * 100}%;"></div>
				{/each}
			{/if}
		</div>
	</div>
	<div class="footer">
		<span class="tag mono" title={member.tag}>{member.tag}</span>
		{#if active}
			<span class="state-badge active-badge">активен</span>
		{:else if switching}
			<span class="state-badge switching-badge">переключаем...</span>
		{/if}
	</div>
</button>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 0.55rem;
		padding: 14px 16px;
		border: 1px solid var(--color-border);
		border-radius: 10px;
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font: inherit;
		text-align: left;
		cursor: pointer;
		transition: border-color 0.15s ease, background 0.15s ease;
	}
	.card:hover:not(.active):not(:disabled) { border-color: var(--color-accent); }
	.card.active { border-color: #3fb950; background: rgba(63, 185, 80, 0.06); }
	.card.switching { opacity: 0.7; cursor: wait; }
	.card:disabled { cursor: wait; opacity: 0.6; }
	.header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.led {
		width: 10px; height: 10px;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		flex-shrink: 0;
	}
	.led.on {
		background: #3fb950;
		box-shadow: 0 0 0 3px rgba(63, 185, 80, 0.22);
	}
	.title {
		font-size: 0.92rem;
		font-weight: 600;
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.port { font-size: 0.78rem; color: var(--color-text-muted); }
	.icon-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		color: var(--color-text-muted);
		cursor: pointer;
	}
	.icon-btn:hover { color: var(--color-accent); }
	.icon-btn svg { width: 14px; height: 14px; }
	.badges { display: flex; gap: 0.4rem; flex-wrap: wrap; }
	.badge {
		font-size: 0.68rem;
		padding: 0.15rem 0.5rem;
		border-radius: 4px;
		font-weight: 600;
		letter-spacing: 0.3px;
	}
	.badge.proto { background: rgba(88,166,255,0.15); color: var(--color-accent); }
	.badge.transport { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
	.badge.tls { background: rgba(63,185,80,0.15); color: #3fb950; }
	.badge.reality { background: rgba(210,153,34,0.15); color: #d29922; }
	.footer {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding-top: 0.4rem;
		border-top: 1px solid var(--color-border);
	}
	.tag {
		font-size: 0.68rem;
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 150px;
	}
	.state-badge {
		font-size: 0.7rem;
		padding: 0.1rem 0.45rem;
		border-radius: 999px;
	}
	.active-badge { background: rgba(63,185,80,0.15); color: #3fb950; }
	.switching-badge { background: rgba(88,166,255,0.15); color: var(--color-accent); }
	.mono { font-family: var(--font-mono, ui-monospace, monospace); }
	.delay-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-top: 0.4rem;
	}
	.delay-btn {
		padding: 0.15rem 0.5rem;
		border-radius: 4px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		border: 1px solid var(--color-border);
		font: inherit;
		font-size: 0.7rem;
		font-family: var(--font-mono, ui-monospace, monospace);
		cursor: pointer;
	}
	.delay-btn.is-disabled { opacity: 0.5; cursor: wait; }
	.delay-btn.ok    { color: #3fb950; }
	.delay-btn.slow  { color: #d29922; }
	.delay-btn.fail  { color: #f85149; }
	.spark {
		flex: 1;
		display: flex;
		gap: 1px;
		align-items: flex-end;
		height: 18px;
	}
	.bar {
		flex: 1;
		background: var(--color-bg-tertiary);
		border-radius: 1px;
	}
	.spark.ok .bar   { background: #3fb950; }
	.spark.slow .bar { background: #d29922; }
	.spark.fail .bar { background: #f85149; }
	.bar.empty       { opacity: 0.3; }
	.server-line {
		font-size: 0.72rem;
		color: var(--color-text-muted);
		opacity: 0.85;
		margin: 0.15rem 0 0.35rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.sni-under-title {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		opacity: 0.85;
		margin-top: 0.15rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
