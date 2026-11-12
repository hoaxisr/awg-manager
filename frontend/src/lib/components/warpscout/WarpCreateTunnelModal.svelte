<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, Modal, SegmentedControl, Toggle } from '$lib/components/ui';
	import type { SegmentedOption } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import type { WarpFilters, WarpHit, WarpProtocol, WarpTargetMode } from '$lib/types/warpscout';
	import { countryFlag, warpErrorMessage } from './warpscoutUi';

	interface Props {
		/** Узел, из которого создаётся туннель. Модалка монтируется на время диалога. */
		hit: WarpHit;
		/** Текущие фильтры сканера: протокол по умолчанию и фильтры профиля автопилота. */
		filters: WarpFilters;
		/** В автопилоте есть несохранённые правки — включать его отсюда нельзя. */
		autopilotLocked: boolean;
		onclose: () => void;
		ondone: () => void | Promise<void>;
	}

	let { hit, filters, autopilotLocked, onclose, ondone }: Props = $props();

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
	let mode = $state<WarpTargetMode>('kernel');
	let protocol = $state<WarpProtocol>(untrack(() => filters.protocol));
	let autopilot = $state(false);
	let applying = $state(false);

	async function submit(): Promise<void> {
		const enableAutopilot = autopilot && !autopilotLocked;
		applying = true;
		try {
			const res = await api.warpscoutApply({
				action: 'create',
				endpoint: hit.endpoint,
				mode,
				name: name.trim() || undefined,
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
		<Button variant="primary" loading={applying} onclick={submit}>
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
</style>
