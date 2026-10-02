<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import type { SingboxRouterDNSServer, SingboxRouterDNSStrategy } from '$lib/types';

	interface Props {
		servers: SingboxRouterDNSServer[];
		final: string;
		strategy: SingboxRouterDNSStrategy;
		timeout?: string;
		onClose: () => void;
		onSave: (globals: {
			final: string;
			strategy: SingboxRouterDNSStrategy;
			timeout: string;
		}) => Promise<void> | void;
	}

	let { servers, final, strategy, timeout = '', onClose, onSave }: Props = $props();

	const STRATEGY_OPTIONS: DropdownOption<SingboxRouterDNSStrategy>[] = [
		{ value: '', label: '— default —' },
		{ value: 'ipv4_only', label: 'ipv4_only' },
		{ value: 'ipv6_only', label: 'ipv6_only' },
		{ value: 'prefer_ipv4', label: 'prefer_ipv4' },
		{ value: 'prefer_ipv6', label: 'prefer_ipv6' },
	];

	const TIMEOUT_OPTIONS = $derived<DropdownOption[]>([
		{ value: '', label: m.routing_singbox_timeout_default() },
		{ value: '3s', label: m.routing_singbox_timeout_seconds({ count: 3 }) },
		{ value: '5s', label: m.routing_singbox_timeout_seconds({ count: 5 }) },
		{ value: '10s', label: m.routing_singbox_timeout_seconds({ count: 10 }) },
		{ value: '15s', label: m.routing_singbox_timeout_seconds({ count: 15 }) },
		{ value: '30s', label: m.routing_singbox_timeout_seconds({ count: 30 }) },
	]);

	const finalOptions = $derived<DropdownOption[]>([
		{ value: '', label: m.routing_singbox_dns_final_unset() },
		...servers.map((s) => ({ value: s.tag, label: s.tag })),
	]);

	// svelte-ignore state_referenced_locally
	let draftFinal = $state(final);
	// svelte-ignore state_referenced_locally
	let draftStrategy = $state<SingboxRouterDNSStrategy>(strategy);
	// svelte-ignore state_referenced_locally
	let draftTimeout = $state(timeout);

	let initialFinal = $state('');
	let initialStrategy = $state<SingboxRouterDNSStrategy>('');
	let initialTimeout = $state('');

	$effect(() => {
		initialFinal = final;
		initialStrategy = strategy;
		initialTimeout = timeout;
		draftFinal = final;
		draftStrategy = strategy;
		draftTimeout = timeout;
	});

	const isDirty = $derived(
		draftFinal !== initialFinal || draftStrategy !== initialStrategy || draftTimeout !== initialTimeout
	);

	let busy = $state(false);
	let error = $state('');

	async function save(): Promise<void> {
		if (!isDirty || busy) return;
		busy = true;
		error = '';
		try {
			await onSave({ final: draftFinal, strategy: draftStrategy, timeout: draftTimeout });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<SingboxSettingsModal
	title={m.routing_singbox_dns_globals_title()}
	onClose={onClose}
	size="md"
	hasUnsavedChanges={() => isDirty}
>
	<div class="form">
		<label class="field">
			<div class="lbl">{m.routing_singbox_dns_final_label()}</div>
			<Dropdown
				bind:value={draftFinal}
				options={finalOptions}
				disabled={servers.length === 0}
				fullWidth
			/>
			<div class="hint">
				{m.routing_singbox_dns_final_hint()}
			</div>
		</label>

		<label class="field">
			<div class="lbl">{m.routing_singbox_dns_strategy()}</div>
			<Dropdown bind:value={draftStrategy} options={STRATEGY_OPTIONS} fullWidth />
			<div class="hint">{m.routing_singbox_dns_strategy_hint()}</div>
		</label>

		<label class="field">
			<div class="lbl">{m.routing_singbox_dns_timeout()}</div>
			<Dropdown bind:value={draftTimeout} options={TIMEOUT_OPTIONS} fullWidth />
			<div class="hint">
				{m.routing_singbox_dns_timeout_hint()}
			</div>
		</label>

		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy || !isDirty} loading={busy} type="button">
			{m.routing_singbox_save()}
		</Button>
	{/snippet}
</SingboxSettingsModal>
