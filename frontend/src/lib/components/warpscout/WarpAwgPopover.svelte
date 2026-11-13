<script lang="ts">
	import { Shield } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button, Dropdown } from '$lib/components/ui';
	import type { DropdownOption } from '$lib/components/ui';
	import type { WarpAWGParams, WarpObfuscationSource } from '$lib/types/warpscout';
	import WarpChipPopover from './WarpChipPopover.svelte';
	import { LIMITS, SIGNATURE_PROFILES, signatureProfileLabel } from './warpscoutUi';

	interface Props {
		awg: WarpAWGParams;
		obfuscationSources?: WarpObfuscationSource[];
		dirty: boolean;
		saving: boolean;
		/** Сохранить параметры AWG; true — сохранено, поповер можно закрыть. */
		onsave: () => Promise<boolean>;
		onreset: () => void;
	}

	let { awg = $bindable(), obfuscationSources = [], dirty, saving, onsave, onreset }: Props = $props();

	const signatureOptions = $derived.by<DropdownOption[]>(() => {
		const opts: DropdownOption[] = SIGNATURE_PROFILES.map((s) => ({
			value: s,
			label: s === 'none' ? m.warpscout_sig_none() : s,
		}));
		if (awg.signatureProfile === 'custom') {
			opts.unshift({ value: 'custom', label: m.warpscout_sig_custom() });
		}
		return opts;
	});

	const copySourceOptions = $derived<DropdownOption[]>([
		{ value: '', label: m.warpscout_awg_copy_placeholder() },
		...obfuscationSources.map((s) => ({
			value: `${s.mode}:${s.id}`,
			label: `${s.name}${s.iface ? ` (${s.iface})` : ''} · Jc ${s.awg.jc}`,
		})),
	]);

	function applyObfSource(key: string): void {
		if (!key) return;
		const src = obfuscationSources.find((s) => `${s.mode}:${s.id}` === key);
		if (!src) return;
		awg = { ...src.awg };
	}

	function changeSignatureProfile(v: string): void {
		awg.signatureProfile = v;
		if (v !== 'custom') {
			awg.i1 = '';
			awg.i2 = '';
			awg.i3 = '';
			awg.i4 = '';
			awg.i5 = '';
		}
	}
</script>

<WarpChipPopover
	title={m.warpscout_awg_pop_title()}
	ariaLabel={m.warpscout_awg_pop_aria()}
	chipTitle={m.warpscout_awg_chip_tip()}
	width="21rem"
>
	{#snippet chip()}
		<Shield size={13} class="chip-ic" />
		<span class="chip-dim hide-sm">{m.warpscout_awg_chip_label()}</span>
		<span class="mono chip-strong">{signatureProfileLabel(awg.signatureProfile)}</span>
		<span class="mono chip-dim">· Jc {awg.jc} · {awg.jmin}–{awg.jmax}</span>
		{#if dirty}<span class="unsaved-dot" title={m.warpscout_unsaved_tip()}></span>{/if}
	{/snippet}

	{#snippet children(close)}
		<div class="awg-pop-body">
			{#if obfuscationSources.length > 0}
				<Dropdown
					label={m.warpscout_awg_copy_label()}
					value=""
					options={copySourceOptions}
					fullWidth
					onchange={applyObfSource}
				/>
			{/if}

			<Dropdown
				label={m.warpscout_awg_sig_label()}
				value={awg.signatureProfile}
				options={signatureOptions}
				fullWidth
				onchange={changeSignatureProfile}
			/>

			<div class="awg-nums">
				<label class="num-field">
					<span>Jc</span>
					<input class="field-input mono" type="number" min={LIMITS.jc.min} max={LIMITS.jc.max} bind:value={awg.jc} />
				</label>
				<label class="num-field">
					<span>Jmin</span>
					<input class="field-input mono" type="number" min={LIMITS.jmin.min} max={LIMITS.jmin.max} bind:value={awg.jmin} />
				</label>
				<label class="num-field">
					<span>Jmax</span>
					<input class="field-input mono" type="number" min={LIMITS.jmax.min} max={LIMITS.jmax.max} bind:value={awg.jmax} />
				</label>
			</div>

			<p class="pop-hint">
				{awg.signatureProfile === 'custom' ? m.warpscout_awg_custom_hint() : m.warpscout_awg_hint()}
			</p>
		</div>

		<div class="pop-foot end">
			{#if dirty}
				<Button size="sm" variant="ghost" onclick={onreset} disabled={saving}>
					{m.warpscout_reset_btn()}
				</Button>
			{/if}
			<Button
				size="sm"
				variant="primary"
				loading={saving}
				disabled={!dirty}
				onclick={async () => {
					if (await onsave()) close();
				}}
			>
				{m.common_save()}
			</Button>
		</div>
	{/snippet}
</WarpChipPopover>

<style>
	.awg-pop-body {
		display: flex;
		flex-direction: column;
		gap: 0.55rem;
	}
	.awg-nums {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.45rem;
	}
	.num-field {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		font-size: 0.72rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.mono {
		font-family: var(--font-mono);
	}
</style>
