<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, Dropdown, Modal, Toggle } from '$lib/components/ui';
	import type { DropdownOption } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import type { WarpFilters, WarpHit, WarpTunnelRef } from '$lib/types/warpscout';
	import {
		countryFlag,
		modeLabel,
		targetKey,
		targetOptionDescription,
		targetSelectable,
		warpErrorMessage,
	} from './warpscoutUi';

	interface Props {
		/** Узел, на который подменяется адрес. Модалка монтируется на время диалога. */
		hit: WarpHit;
		targets: WarpTunnelRef[];
		/** Текущие фильтры сканера: протокол и фильтры профиля автопилота. */
		filters: WarpFilters;
		/** В автопилоте есть несохранённые правки — включать его отсюда нельзя. */
		autopilotLocked: boolean;
		onclose: () => void;
		ondone: () => void | Promise<void>;
	}

	let { hit, targets, filters, autopilotLocked, onclose, ondone }: Props = $props();

	const options = $derived.by((): DropdownOption[] =>
		[...targets]
			.sort((a, b) => Number(targetSelectable(b)) - Number(targetSelectable(a)))
			.map((t) => ({
				value: targetKey(t.mode, t.id),
				label: `${t.name}${t.iface ? ` (${t.iface})` : ''}`,
				description: targetOptionDescription(t),
				disabled: !targetSelectable(t),
				group: modeLabel(t.mode),
			})),
	);
	const anyTarget = $derived(targets.some(targetSelectable));

	let target = $state(
		untrack(() => {
			const first = targets.find(targetSelectable);
			return first ? targetKey(first.mode, first.id) : '';
		}),
	);
	let autopilot = $state(false);
	let applying = $state(false);

	async function submit(): Promise<void> {
		const t = targets.find((x) => targetKey(x.mode, x.id) === target);
		if (!t) return;
		const enableAutopilot = autopilot && !autopilotLocked;
		applying = true;
		try {
			await api.warpscoutApply({
				action: 'swap',
				endpoint: hit.endpoint,
				colo: hit.colo || undefined,
				mode: t.mode,
				targetId: t.id,
				protocol: filters.protocol,
				enableAutopilot,
				filters: enableAutopilot ? { ...filters } : undefined,
			});
			notifications.success(m.warpscout_swapped_ok({ name: t.name, endpoint: hit.endpoint }));
			onclose();
			await ondone();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			applying = false;
		}
	}
</script>

<Modal open title={m.warpscout_modal_swap_title()} size="md" {onclose}>
	<div class="modal-body">
		<p class="note">
			{m.warpscout_modal_new_ep()} <span class="mono">{hit.endpoint}</span>
			{#if hit.colo}· {hit.colo}{/if}
			{#if hit.loc}{countryFlag(hit.loc)} {hit.loc}{/if}
		</p>
		{#if targets.length === 0}
			<p class="note warn">{m.warpscout_no_tunnels()}</p>
		{:else}
			<Dropdown
				label={m.warpscout_field_tunnel()}
				value={target}
				{options}
				fullWidth
				onchange={(v) => (target = v)}
			/>
			{#if !anyTarget}
				<p class="note warn">{m.warpscout_no_warp_tunnels()}</p>
			{/if}
		{/if}
		<Toggle
			checked={autopilot && !autopilotLocked}
			size="sm"
			label={m.warpscout_enable_auto_swap()}
			hint={autopilotLocked ? m.warpscout_autopilot_locked_hint() : ''}
			disabled={autopilotLocked}
			onchange={(v) => (autopilot = v)}
		/>
	</div>
	{#snippet actions()}
		<Button variant="ghost" onclick={onclose} disabled={applying}>
			{m.common_cancel()}
		</Button>
		<Button variant="primary" loading={applying} disabled={!target || !anyTarget} onclick={submit}>
			{m.warpscout_swap_btn()}
		</Button>
	{/snippet}
</Modal>

<style>
	.modal-body {
		display: flex;
		flex-direction: column;
		gap: 0.8rem;
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
