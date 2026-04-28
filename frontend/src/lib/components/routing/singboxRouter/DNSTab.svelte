<script lang="ts">
	import type {
		SingboxRouterDNSServer,
		SingboxRouterDNSRule,
		SingboxRouterDNSGlobals,
	} from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import DNSGlobals from './DNSGlobals.svelte';
	import DNSServersList from './DNSServersList.svelte';
	import DNSRulesList from './DNSRulesList.svelte';

	interface Props {
		servers: SingboxRouterDNSServer[];
		rules: SingboxRouterDNSRule[];
		globals: SingboxRouterDNSGlobals;
		outboundOptions: OutboundGroup[];
		onChange: () => Promise<void> | void;
	}
	let { servers, rules, globals, outboundOptions, onChange }: Props = $props();

	type DNSSection = 'servers' | 'rules';
	let section = $state<DNSSection>('servers');
</script>

<DNSGlobals {globals} {servers} {onChange} />

<div class="section-tabs">
	<button class:active={section === 'servers'} onclick={() => (section = 'servers')} type="button">
		Серверы <span class="count">{servers.length}</span>
	</button>
	<button class:active={section === 'rules'} onclick={() => (section = 'rules')} type="button">
		Правила <span class="count">{rules.length}</span>
	</button>
</div>

<div class="section-content">
	{#if section === 'servers'}
		<DNSServersList {servers} {outboundOptions} {onChange} />
	{:else}
		<DNSRulesList {rules} {servers} finalLabel={globals.final} {onChange} />
	{/if}
</div>

<style>
	.section-tabs {
		display: flex;
		gap: 0.2rem;
		margin: 0.75rem 0 0.6rem;
		border-bottom: 1px solid var(--border);
	}
	.section-tabs button {
		background: transparent;
		border: none;
		padding: 0.5rem 0.9rem;
		cursor: pointer;
		color: var(--muted-text);
		font-size: 0.85rem;
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		border-bottom: 2px solid transparent;
	}
	.section-tabs button.active {
		color: var(--text);
		border-bottom-color: var(--accent, #3b82f6);
		font-weight: 600;
	}
	.count {
		font-size: 0.7rem;
		color: var(--muted-text);
		background: var(--surface-bg);
		padding: 0.1rem 0.35rem;
		border-radius: 10px;
	}
	.section-content {
		padding-top: 0.25rem;
	}
</style>
