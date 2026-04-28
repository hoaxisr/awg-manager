<script lang="ts">
	import { onMount } from 'svelte';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxTunnels } from '$lib/stores/singbox';
	import type { AWGTagInfo, SingboxTunnel } from '$lib/types';
	import { api } from '$lib/api/client';
	import {
		NetfilterMissingBanner,
		EngineStatusCard,
		RefreshSettingsModal,
		RulesList,
		RuleSetsList,
		CompositeOutboundsList,
		PresetsGallery,
		DNSTab,
		buildOutboundOptions,
	} from '$lib/components/routing/singboxRouter';

	// routingTunnels prop kept for backwards compatibility with parent
	// page; the AWG outbound picker now sources its tags from the
	// canonical /api/singbox/awg-outbounds/tags endpoint, which already
	// covers both managed and system tunnels with proper labels.
	interface Props {
		routingTunnels: Array<{ id: string; name?: string }>;
	}
	let { routingTunnels: _ }: Props = $props();

	let awgTags = $state<AWGTagInfo[]>([]);

	async function loadAWGTags(): Promise<void> {
		try {
			awgTags = await api.getAWGTags();
		} catch {
			awgTags = [];
		}
	}

	type SubTab = 'rules' | 'rulesets' | 'outbounds' | 'dns' | 'presets';
	let activeSubTab = $state<SubTab>('rules');
	let showRefreshModal = $state(false);

	const statusStore = singboxRouter.status;
	const settingsStore = singboxRouter.settings;
	const rulesStore = singboxRouter.rules;
	const ruleSetsStore = singboxRouter.ruleSets;
	const outboundsStore = singboxRouter.outbounds;
	const presetsStore = singboxRouter.presets;
	const dnsServersStore = singboxRouter.dnsServers;
	const dnsRulesStore = singboxRouter.dnsRules;
	const dnsGlobalsStore = singboxRouter.dnsGlobals;
	const phase1TunnelsStore = singboxTunnels;

	const status = $derived($statusStore);
	const settings = $derived($settingsStore);
	const rules = $derived($rulesStore);
	const ruleSets = $derived($ruleSetsStore);
	const outbounds = $derived($outboundsStore);
	const presets = $derived($presetsStore);
	const dnsServers = $derived($dnsServersStore);
	const dnsRules = $derived($dnsRulesStore);
	const dnsGlobals = $derived($dnsGlobalsStore);
	const phase1Tunnels = $derived(($phase1TunnelsStore.data ?? []) as SingboxTunnel[]);

	const outboundOptions = $derived(
		buildOutboundOptions(awgTags, phase1Tunnels, outbounds, true)
	);

	async function refresh(): Promise<void> {
		await singboxRouter.loadAll();
	}

	onMount(() => {
		refresh();
		loadAWGTags();
		const hash = typeof window !== 'undefined' ? window.location.hash.replace('#', '') : '';
		if (hash === 'rules' || hash === 'rulesets' || hash === 'outbounds' || hash === 'dns' || hash === 'presets') {
			activeSubTab = hash;
		}
	});

	function setSubTab(t: SubTab): void {
		activeSubTab = t;
		if (typeof window !== 'undefined') {
			history.replaceState(null, '', `#${t}`);
		}
	}
</script>

{#if status && !status.netfilterAvailable}
	<NetfilterMissingBanner componentName={status.netfilterComponentName} />
{/if}

<EngineStatusCard
	{status}
	{settings}
	onChange={refresh}
	onOpenRefreshSettings={() => (showRefreshModal = true)}
/>

{#if status}
	{#if !status.enabled}
		<div class="disabled-hint">
			Движок выключен. Настройте правила/rule sets/outbounds сейчас —
			они вступят в силу после включения.
		</div>
	{/if}

	<div class="sub-tabs">
		<button class:active={activeSubTab === 'rules'} onclick={() => setSubTab('rules')} type="button">
			Правила <span class="count">{status.ruleCount}</span>
		</button>
		<button class:active={activeSubTab === 'rulesets'} onclick={() => setSubTab('rulesets')} type="button">
			Rule sets <span class="count">{status.ruleSetCount}</span>
		</button>
		<button class:active={activeSubTab === 'outbounds'} onclick={() => setSubTab('outbounds')} type="button">
			Composite outbounds <span class="count">{status.outboundCompositeCount}</span>
		</button>
		<button class:active={activeSubTab === 'dns'} onclick={() => setSubTab('dns')} type="button">
			DNS <span class="count">{dnsServers.length}/{dnsRules.length}</span>
		</button>
		<button class:active={activeSubTab === 'presets'} onclick={() => setSubTab('presets')} type="button">
			Пресеты
		</button>
	</div>

	<div class="sub-content">
		{#if activeSubTab === 'rules'}
			<RulesList
				{rules}
				{outboundOptions}
				finalLabel={status.final || 'direct'}
				onChange={refresh}
			/>
		{:else if activeSubTab === 'rulesets'}
			<RuleSetsList {ruleSets} {outboundOptions} onChange={refresh} />
		{:else if activeSubTab === 'outbounds'}
			<CompositeOutboundsList {outbounds} {outboundOptions} onChange={refresh} />
		{:else if activeSubTab === 'dns'}
			<DNSTab
				servers={dnsServers}
				rules={dnsRules}
				globals={dnsGlobals}
				{outboundOptions}
				onChange={refresh}
			/>
		{:else if activeSubTab === 'presets'}
			<PresetsGallery {presets} {outboundOptions} onApplied={refresh} />
		{/if}
	</div>
{/if}

{#if showRefreshModal && settings}
	<RefreshSettingsModal
		{settings}
		onClose={() => (showRefreshModal = false)}
		onSaved={refresh}
	/>
{/if}

<style>
	.sub-tabs {
		display: flex;
		gap: 0.2rem;
		margin: 1rem 0 0.75rem;
		border-bottom: 1px solid var(--border);
	}
	.sub-tabs button {
		background: transparent;
		border: none;
		padding: 0.55rem 1rem;
		cursor: pointer;
		color: var(--muted-text);
		font-size: 0.9rem;
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		border-bottom: 2px solid transparent;
	}
	.sub-tabs button.active {
		color: var(--text);
		border-bottom-color: var(--accent, #3b82f6);
		font-weight: 600;
	}
	.count {
		font-size: 0.75rem;
		color: var(--muted-text);
		background: var(--surface-bg);
		padding: 0.1rem 0.4rem;
		border-radius: 10px;
	}
	.sub-tabs button.active .count {
		color: var(--text);
	}
	.sub-content {
		padding-top: 0.25rem;
	}
	.disabled-hint {
		padding: 0.6rem 0.9rem;
		margin: 0.75rem 0;
		background: rgba(122, 162, 247, 0.08);
		border-left: 3px solid var(--accent, #3b82f6);
		border-radius: 4px;
		color: var(--muted-text);
		font-size: 0.85rem;
		line-height: 1.4;
	}
</style>
