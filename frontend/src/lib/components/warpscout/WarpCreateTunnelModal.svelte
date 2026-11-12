<script lang="ts">
	import { untrack } from 'svelte';
	import { AlertTriangle } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, Modal, SegmentedControl, Toggle } from '$lib/components/ui';
	import type { SegmentedOption } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import type {
		WarpFilters,
		WarpHit,
		WarpProtocol,
		WarpTargetMode,
		WarpTunnelRef,
	} from '$lib/types/warpscout';
	import {
		countryFlag,
		findTunnelIpv4Conflict,
		parseTunnelIpv4,
		suggestFreeTunnelIpv4,
		warpErrorMessage,
	} from './warpscoutUi';

	interface Props {
		/** Узел, из которого создаётся туннель. Модалка монтируется на время диалога. */
		hit: WarpHit;
		/** Текущие фильтры сканера: протокол по умолчанию и фильтры профиля автопилота. */
		filters: WarpFilters;
		/** Адрес IPv4 по умолчанию из учётной записи WARP. */
		accountAddressV4?: string;
		/** Известные туннели и занятые IPv4-адреса роутера для проверки конфликтов. */
		targets?: WarpTunnelRef[];
		usedIpv4?: string[];
		/** В автопилоте есть несохранённые правки — включать его отсюда нельзя. */
		autopilotLocked: boolean;
		onclose: () => void;
		ondone: () => void | Promise<void>;
	}

	let {
		hit,
		filters,
		accountAddressV4 = '',
		targets = [],
		usedIpv4 = [],
		autopilotLocked,
		onclose,
		ondone,
	}: Props = $props();

	const modeOptions = $derived<SegmentedOption<WarpTargetMode>[]>([
		{ value: 'kernel', label: 'Kernel AWG' },
		{ value: 'native', label: 'Native WG' },
		{ value: 'system', label: m.warpscout_mode_system_short() },
	]);
	const protocolOptions = $derived<SegmentedOption<WarpProtocol>[]>([
		{ value: 'awg', label: 'AmneziaWG' },
		{ value: 'wg', label: 'WG' },
	]);

	let name = $state(untrack(() => `WARP ${hit.colo || ''}`.trim()));
	let addressV4 = $state(untrack(() => accountAddressV4 || '172.16.0.2'));
	let mode = $state<WarpTargetMode>('kernel');
	let protocol = $state<WarpProtocol>(untrack(() => filters.protocol));
	let autopilot = $state(false);
	let applying = $state(false);

	const parsedIp = $derived(parseTunnelIpv4(addressV4));
	const addrValid = $derived(parsedIp !== null);
	const addrConflict = $derived(findTunnelIpv4Conflict(addressV4, targets, usedIpv4));
	const suggestedIp = $derived(
		addrConflict.conflict ? suggestFreeTunnelIpv4(addressV4, targets, usedIpv4) : '',
	);

	async function submit(): Promise<void> {
		if (!parsedIp) return;
		const enableAutopilot = autopilot && !autopilotLocked;
		applying = true;
		try {
			const res = await api.warpscoutApply({
				action: 'create',
				endpoint: hit.endpoint,
				mode,
				name: name.trim() || undefined,
				addressV4: parsedIp.normalized,
				protocol,
				enableAutopilot,
				filters: enableAutopilot ? { ...filters, protocol } : undefined,
			});
			const created = res.tunnel?.name || res.tunnel?.id || name;
			notifications.success(
				enableAutopilot && res.profileId
					? m.warpscout_created_with_auto({ name: created })
					: m.warpscout_created_ok({ name: created }),
			);
			onclose();
			await ondone();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			applying = false;
		}
	}
</script>

<Modal open title={m.warpscout_modal_create_title()} size="md" {onclose}>
	<div class="modal-body">
		<p class="note">
			{m.warpscout_modal_node()} <span class="mono">{hit.endpoint}</span>
			{#if hit.colo}· {hit.colo}{/if}
			{#if hit.loc}{countryFlag(hit.loc)} {hit.loc}{/if}
		</p>
		<label class="stack">
			<span class="ws-label">{m.warpscout_field_name()}</span>
			<input class="field-input" type="text" bind:value={name} maxlength={64} />
		</label>
		<label class="stack">
			<span class="ws-label">{m.warpscout_acct_ipv4()}</span>
			<input
				class="field-input mono"
				type="text"
				placeholder="172.16.0.2"
				bind:value={addressV4}
				maxlength={20}
			/>
			{#if addressV4.trim() && !addrValid}
				<span class="ipv4-msg err">{m.warpscout_ipv4_invalid()}</span>
			{:else if addrConflict.conflict}
				<div class="ipv4-msg warn">
					<AlertTriangle size={12} />
					<span>
						{#if addrConflict.tunnelName}
							{m.warpscout_ipv4_conflict_named({
								ip: addrConflict.host,
								name: addrConflict.tunnelName,
							})}
						{:else}
							{m.warpscout_ipv4_conflict_generic({ ip: addrConflict.host })}
						{/if}
						{#if suggestedIp && suggestedIp !== addressV4.trim()}
							<button
								type="button"
								class="ipv4-suggest-btn"
								onclick={() => (addressV4 = suggestedIp)}
							>
								{m.warpscout_ipv4_use_free({ ip: suggestedIp })}
							</button>
						{/if}
					</span>
				</div>
			{/if}
		</label>
		<div class="stack">
			<span class="ws-label">{m.warpscout_field_tunnel_mode()}</span>
			<SegmentedControl
				value={mode}
				options={modeOptions}
				ariaLabel={m.warpscout_field_tunnel_mode()}
				onchange={(v) => (mode = v)}
			/>
		</div>
		<div class="stack">
			<span class="ws-label">{m.warpscout_field_protocol()}</span>
			<SegmentedControl
				value={protocol}
				options={protocolOptions}
				ariaLabel={m.warpscout_field_protocol()}
				onchange={(v) => (protocol = v)}
			/>
		</div>
		<Toggle
			checked={autopilot && !autopilotLocked}
			size="sm"
			label={m.warpscout_enable_auto_create()}
			hint={autopilotLocked ? m.warpscout_autopilot_locked_hint() : ''}
			disabled={autopilotLocked}
			onchange={(v) => (autopilot = v)}
		/>
	</div>
	{#snippet actions()}
		<Button variant="ghost" onclick={onclose} disabled={applying}>
			{m.common_cancel()}
		</Button>
		<Button variant="primary" loading={applying} disabled={!addrValid} onclick={submit}>
			{m.common_create()}
		</Button>
	{/snippet}
</Modal>

<style>
	.modal-body {
		display: flex;
		flex-direction: column;
		gap: 0.8rem;
	}
	.stack {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}
	.ws-label {
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.note {
		margin: 0;
		font-size: 0.76rem;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}
	.mono {
		font-family: var(--font-mono);
	}
	.ipv4-msg {
		display: flex;
		align-items: flex-start;
		gap: 0.3rem;
		font-size: 0.7rem;
		line-height: 1.35;
	}
	.ipv4-msg :global(svg) {
		flex-shrink: 0;
		margin-top: 0.1rem;
	}
	.ipv4-msg.err {
		color: var(--color-error);
	}
	.ipv4-msg.warn {
		color: var(--color-warning);
	}
	.ipv4-suggest-btn {
		margin-left: 0.25rem;
		padding: 0;
		border: 0;
		background: transparent;
		color: var(--color-accent);
		font: inherit;
		font-weight: 600;
		text-decoration: underline;
		cursor: pointer;
	}
</style>
