<script lang="ts">
	import type { SubscriptionMember } from '$lib/types';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import { PingButton } from '$lib/components/ui';
	import { singboxDelayHistory, triggerDelayCheck } from '$lib/stores/singbox';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';

	interface Props {
		member: SubscriptionMember;
		active: boolean;
		switching: boolean;
		disabled: boolean;
		onclick: () => void;
		layout?: SingboxLayoutMode;
	}
	let { member, active, switching, disabled, onclick, layout = 'compact' }: Props = $props();

	const history = $derived($singboxDelayHistory.get(member.tag) ?? []);
	const delayPresentation = $derived(singboxDelayFromHistory(history));
	const latest = $derived(delayPresentation.latest ?? -1);

	let testing = $state(false);

	async function runTest(e?: MouseEvent | KeyboardEvent): Promise<void> {
		e?.stopPropagation(); // don't trigger card-as-radio click
		if (testing) return;
		testing = true;
		try {
			await triggerDelayCheck(member.tag);
		} finally {
			testing = false;
		}
	}
	function onSparkKeydown(e: KeyboardEvent): void {
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			void runTest(e);
		}
	}
	const delayState = $derived(delayPresentation.state);
	const delayText = $derived(delayPresentation.label);

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

	const heading = $derived(member.label || member.server);
</script>

{#if layout === 'list'}
	<div class="mbr-flatten">
		<div class="c c-delay" data-label="Delay">
			<PingButton
				label={delayText}
				state={delayState}
				checking={testing}
				size="mid"
				forceBorder
				onclick={runTest}
			/>
		</div>
		<div class="c c-name" data-label="Сервер">
			<span class="n1" title={heading}>{heading}</span>
			<span class="n2 mono" title={member.tag}>{member.server}:{member.port}</span>
		</div>
	<div class="c c-badges" data-label="Протокол">
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
		<div class="c c-ping-mini" data-label="Ping">
			<div
				class="spark-mini {delayState}"
				role="button"
				tabindex="0"
				onclick={(e) => runTest(e)}
				onkeydown={onSparkKeydown}
				title="Клик — обновить delay"
			>
				{#if history.length === 0}
					{#each Array(10) as _, i (i)}
						<div class="bar empty"></div>
					{/each}
				{:else}
					{@const max = Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100)}
					{#each history.slice(-14) as d, i (i)}
						<div class="bar" style="height: {Math.max((d <= 0 ? max : d) / max, 0.08) * 100}%;"></div>
					{/each}
				{/if}
			</div>
		</div>
		<div class="c mono c-tag" data-label="Тег">{member.tag}</div>
		<div class="c c-state" data-label="">
			{#if active}
				<span class="state-badge active-badge">активен</span>
			{:else if switching}
				<span class="state-badge switching-badge">…</span>
			{/if}
		</div>
	</div>
{:else}
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
		<span class="title" title={heading}>{heading}</span>
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
	{#if member.label}
		<div class="server-line mono" title={member.tag}>{member.server}:{member.port}</div>
	{/if}
	{#if member.sni}
		<div class="sni-row">
			<span class="sni-label">SNI</span>
			<span class="sni-value mono" title={member.sni}>{member.sni}</span>
		</div>
	{/if}
	<div class="delay-row">
		<PingButton
			label={delayText}
			state={delayState}
			checking={testing}
			size="mid"
			forceBorder
			title="Проверить delay"
			onclick={runTest}
		/>
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
{/if}

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 0.55rem;
		width: 100%;
		min-width: 0;
		min-height: 220px;
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
		font-size: var(--sbx-card-title);
		font-weight: 600;
		flex: 1;
		min-width: 0;
		overflow: hidden;
		display: -webkit-box;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		white-space: normal;
		word-break: break-word;
		overflow-wrap: anywhere;
	}
	.port { font-size: var(--sbx-card-meta); color: var(--color-text-muted); }
	.badges { display: flex; gap: 0.4rem; flex-wrap: wrap; }
	.badge {
		font-size: var(--sbx-card-badge);
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
		font-size: var(--sbx-card-badge);
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 150px;
	}
	.state-badge {
		font-size: var(--sbx-card-note);
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
	.spark.ok .bar   { background: var(--latency-bar-ok); }
	.spark.slow .bar { background: var(--latency-bar-slow); }
	.spark.fail .bar { background: var(--latency-bar-fail); }
	.bar.empty       { opacity: 0.3; }
	.server-line {
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		opacity: 0.85;
		margin: 0.15rem 0 0.35rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.sni-row {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		font-size: var(--sbx-card-label);
		color: var(--color-text-muted);
		margin-top: -0.15rem;
	}
	.sni-label {
		text-transform: uppercase;
		letter-spacing: 0.4px;
		opacity: 0.85;
	}
	.sni-value {
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	@media (max-width: 640px) {
		.card {
			padding: 13px 14px;
			min-height: 0;
		}
	}

	.c {
		display: flex;
		align-items: center;
		min-width: 0;
		padding: 0.65rem 0;
		font-size: var(--sbx-card-value);
		color: var(--color-text-secondary);
	}
	.c-name {
		flex-direction: column;
		align-items: flex-start !important;
		gap: 0.12rem;
	}
	.n1 {
		font-weight: 600;
		color: var(--color-text-primary);
		font-size: var(--sbx-card-title);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 100%;
	}
	.n2 {
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 100%;
	}
	.c-badges {
		gap: 0.3rem;
		flex-wrap: wrap;
	}
	.c-tag {
		font-size: var(--sbx-card-meta);
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.c-ping-mini {
		padding-left: 0;
		padding-right: 0;
	}
	.spark-mini {
		display: flex;
		align-items: flex-end;
		gap: 1px;
		height: 20px;
		width: 100%;
		max-width: 82px;
		cursor: pointer;
	}
	.spark-mini .bar {
		flex: 1;
		min-width: 0;
		min-height: 2px;
		border-radius: 1px;
		background: var(--color-bg-tertiary);
	}
	.spark-mini.ok .bar {
		background: var(--latency-bar-ok);
	}
	.spark-mini.slow .bar {
		background: var(--latency-bar-slow);
	}
	.spark-mini.fail .bar {
		background: var(--latency-bar-fail);
	}
	.spark-mini.unknown .bar,
	.spark-mini .bar.empty {
		opacity: 0.35;
		height: 30% !important;
	}
</style>
