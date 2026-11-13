<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, Dropdown, Modal, SegmentedControl, Toggle } from '$lib/components/ui';
	import type { DropdownOption, SegmentedOption } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import type {
		WarpAWGParams,
		WarpFilters,
		WarpHit,
		WarpObfuscationSource,
		WarpProtocol,
		WarpRouteRef,
		WarpTargetMode,
		WarpTunnelRef,
	} from '$lib/types/warpscout';
	import WarpIpv4Notice from './WarpIpv4Notice.svelte';
	import {
		countryFlag,
		parseTunnelIpv4,
		signatureProfileLabel,
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
		routes?: WarpRouteRef[];
		scanIface?: string;
		obfuscationSources?: WarpObfuscationSource[];
		globalAwg?: WarpAWGParams;
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
		routes = [],
		scanIface = '',
		obfuscationSources = [],
		globalAwg,
		autopilotLocked,
		onclose,
		ondone,
	}: Props = $props();

	const warpRoutes = $derived(
		routes.filter((r) => r.kind === 'warp' && !!r.tunnelId && !!r.tunnelMode),
	);
	const initialVia = untrack(() => {
		const r = routes.find((x) => x.name === scanIface && x.kind === 'warp' && !!x.tunnelId && !!x.tunnelMode);
		return r ? `${r.tunnelMode}:${r.tunnelId}` : '';
	});
	const defaultDirectName = untrack(() => `WARP ${hit.colo || ''}`.trim());
	const defaultCascadeName = untrack(() => `WARP-in-WARP ${hit.colo || ''}`.trim());

	let viaKey = $state(initialVia);
	const viaRoute = $derived(
		warpRoutes.find((r) => `${r.tunnelMode}:${r.tunnelId}` === viaKey),
	);

	const modeOptions = $derived<SegmentedOption<WarpTargetMode>[]>([
		{ value: 'kernel', label: 'Kernel AWG', disabled: !!viaRoute },
		{ value: 'native', label: 'Native WG' },
		{ value: 'system', label: m.warpscout_mode_system_short() },
	]);
	const protocolOptions = $derived<SegmentedOption<WarpProtocol>[]>([
		{ value: 'awg', label: 'AmneziaWG' },
		{ value: 'wg', label: 'WG' },
	]);
	const viaOptions = $derived<DropdownOption[]>([
		{ value: '', label: m.warpscout_modal_via_direct() },
		...warpRoutes.map((r) => ({
			value: `${r.tunnelMode}:${r.tunnelId}`,
			label: m.warpscout_modal_via_warp({ name: r.label || r.name, iface: r.name }),
		})),
	]);
	const obfOptions = $derived<DropdownOption[]>([
		{
			value: '',
			label: m.warpscout_modal_obf_global({
				label: `${signatureProfileLabel(globalAwg?.signatureProfile)} · Jc ${globalAwg?.jc ?? 0}`,
			}),
		},
		...obfuscationSources.map((s) => ({
			value: `${s.mode}:${s.id}`,
			label: `${s.name}${s.iface ? ` (${s.iface})` : ''} · Jc ${s.awg.jc}`,
		})),
	]);

	let name = $state(initialVia ? defaultCascadeName : defaultDirectName);
	let nameEdited = $state(false);
	let addressV4 = $state(
		untrack(() =>
			initialVia
				? suggestFreeTunnelIpv4('172.16.0.3', targets, usedIpv4)
				: accountAddressV4 || '172.16.0.2',
		),
	);
	let mode = $state<WarpTargetMode>(initialVia ? 'native' : 'kernel');
	let protocol = $state<WarpProtocol>(untrack(() => filters.protocol));
	let obfSourceKey = $state('');
	let autopilot = $state(false);
	let applying = $state(false);

	const selectedObf = $derived(
		obfuscationSources.find((s) => `${s.mode}:${s.id}` === obfSourceKey),
	);
	const isCascadeKernel = $derived(!!viaRoute && mode === 'kernel');
	const isNativePlainWg = $derived(mode === 'native' && protocol === 'wg');

	function onViaChange(next: string): void {
		viaKey = next;
		const isCascade = warpRoutes.some((r) => `${r.tunnelMode}:${r.tunnelId}` === next);
		if (isCascade && mode === 'kernel') mode = 'native';
		if (!nameEdited) {
			name = isCascade ? defaultCascadeName : defaultDirectName;
		}
		if (isCascade) {
			addressV4 = suggestFreeTunnelIpv4('172.16.0.3', targets, usedIpv4);
		}
	}

	const parsedIp = $derived(parseTunnelIpv4(addressV4));
	const addrValid = $derived(parsedIp !== null);

	async function submit(): Promise<void> {
		if (!parsedIp || isCascadeKernel || isNativePlainWg) return;
		const enableAutopilot = autopilot && !autopilotLocked;
		applying = true;
		try {
			const res = await api.warpscoutApply({
				action: 'create',
				endpoint: hit.endpoint,
				colo: hit.colo || undefined,
				mode,
				name: name.trim() || undefined,
				addressV4: parsedIp.normalized,
				protocol,
				viaMode: viaRoute?.tunnelMode,
				viaId: viaRoute?.tunnelId,
				awg: protocol === 'awg' && selectedObf ? selectedObf.awg : undefined,
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
			<input
				class="field-input"
				type="text"
				bind:value={name}
				oninput={() => (nameEdited = true)}
				maxlength={64}
			/>
		</label>
		{#if warpRoutes.length > 0}
			<div class="stack">
				<Dropdown
					label={m.warpscout_modal_via_label()}
					value={viaKey}
					options={viaOptions}
					fullWidth
					onchange={onViaChange}
				/>
				{#if viaRoute}
					<p class="note">{m.warpscout_modal_via_hint({ name: viaRoute.label || viaRoute.name })}</p>
				{/if}
			</div>
		{/if}
		<label class="stack">
			<span class="ws-label">{m.warpscout_acct_ipv4()}</span>
			<input
				class="field-input mono"
				type="text"
				placeholder="172.16.0.2"
				bind:value={addressV4}
				maxlength={20}
			/>
			<WarpIpv4Notice
				value={addressV4}
				{targets}
				{usedIpv4}
				onsuggest={(ip) => (addressV4 = ip)}
			/>
		</label>
		<div class="stack">
			<span class="ws-label">{m.warpscout_field_tunnel_mode()}</span>
			<SegmentedControl
				value={mode}
				options={modeOptions}
				ariaLabel={m.warpscout_field_tunnel_mode()}
				onchange={(v) => (mode = v)}
			/>
			{#if isCascadeKernel}
				<p class="note warn">{m.warpscout_modal_via_kernel_warn()}</p>
			{/if}
		</div>
		<div class="stack">
			<span class="ws-label">{m.warpscout_field_protocol()}</span>
			<SegmentedControl
				value={protocol}
				options={protocolOptions}
				ariaLabel={m.warpscout_field_protocol()}
				onchange={(v) => (protocol = v)}
			/>
			{#if isNativePlainWg}
				<p class="note warn">{m.warpscout_modal_native_wg_warn()}</p>
			{/if}
		</div>
		{#if protocol === 'awg' && obfuscationSources.length > 0}
			<Dropdown
				label={m.warpscout_modal_obf_label()}
				value={obfSourceKey}
				options={obfOptions}
				fullWidth
				onchange={(v) => (obfSourceKey = v)}
			/>
		{/if}
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
		<Button
			variant="primary"
			loading={applying}
			disabled={!addrValid || isCascadeKernel || isNativePlainWg}
			onclick={submit}
		>
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
	.note.warn {
		color: var(--color-warning);
	}
	.mono {
		font-family: var(--font-mono);
	}
</style>
