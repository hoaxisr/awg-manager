<script lang="ts">
	import { m } from '$lib/i18n';
	import { Dropdown } from '$lib/components/ui';
	import type { DropdownOption } from '$lib/components/ui';
	import type { WarpProfile, WarpTargetMode, WarpTunnelRef } from '$lib/types/warpscout';
	import WarpFiltersEditor from './WarpFiltersEditor.svelte';
	import { LIMITS, modeLabel, targetKey, targetOptionDescription, targetSelectable } from './warpscoutUi';

	interface Props {
		profile: WarpProfile;
		targets: WarpTunnelRef[];
		idPrefix: string;
	}

	let { profile = $bindable(), targets, idPrefix }: Props = $props();

	const MODE_ORDER: WarpTargetMode[] = ['kernel', 'native', 'system'];

	const targetOptions = $derived.by((): DropdownOption[] => {
		const sorted = [...targets].sort(
			(a, b) => MODE_ORDER.indexOf(a.mode) - MODE_ORDER.indexOf(b.mode) || a.name.localeCompare(b.name),
		);
		const opts: DropdownOption[] = sorted.map((t) => ({
			value: targetKey(t.mode, t.id),
			label: `${t.name}${t.iface && t.iface !== t.name ? ` (${t.iface})` : ''}`,
			description: targetOptionDescription(t),
			disabled: !targetSelectable(t),
			group: modeLabel(t.mode),
		}));
		if (profile.targetId && !targets.some((t) => t.mode === profile.targetMode && t.id === profile.targetId)) {
			opts.unshift({
				value: targetKey(profile.targetMode, profile.targetId),
				label: m.warpscout_target_missing_opt({ id: profile.targetId }),
				description: m.warpscout_target_missing_desc(),
				group: modeLabel(profile.targetMode),
			});
		}
		return opts;
	});

	function setTarget(key: string): void {
		const ix = key.indexOf(':');
		if (ix <= 0) return;
		profile.targetMode = key.slice(0, ix) as WarpTargetMode;
		profile.targetId = key.slice(ix + 1);
	}
</script>

<div class="editor-drawer">
	<div class="editor-top">
		<label class="field-col">
			<span>{m.warpscout_profile_name()}</span>
			<input
				class="field-input"
				type="text"
				bind:value={profile.name}
				maxlength={64}
				placeholder={m.warpscout_profile_name_ph()}
			/>
		</label>
		<div class="target-col">
			<Dropdown
				label={m.warpscout_target_tunnel()}
				value={profile.targetId ? targetKey(profile.targetMode, profile.targetId) : ''}
				options={targetOptions}
				placeholder={m.warpscout_target_tunnel_ph()}
				fullWidth
				onchange={setTarget}
			/>
		</div>
		<label class="num-field">
			<span>{m.warpscout_interval_sec()}</span>
			<input
				class="field-input mono"
				type="number"
				min={LIMITS.checkIntervalSec.min}
				max={LIMITS.checkIntervalSec.max}
				bind:value={profile.checkIntervalSec}
			/>
		</label>
		<label class="num-field">
			<span>{m.warpscout_fail_threshold()}</span>
			<input
				class="field-input mono"
				type="number"
				min={LIMITS.failThreshold.min}
				max={LIMITS.failThreshold.max}
				bind:value={profile.failThreshold}
			/>
		</label>
		<label class="num-field" title={m.warpscout_cooldown_tip()}>
			<span>{m.warpscout_cooldown_sec()}</span>
			<input
				class="field-input mono"
				type="number"
				min={LIMITS.cooldownSec.min}
				max={LIMITS.cooldownSec.max}
				bind:value={profile.cooldownSec}
			/>
		</label>
	</div>

	<div class="editor-filters">
		<WarpFiltersEditor bind:filters={profile.filters} {idPrefix} />
	</div>
</div>

<style>
	.editor-drawer {
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
		padding: 0.65rem 0.75rem;
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-secondary) 75%, transparent);
	}
	.editor-top {
		display: grid;
		grid-template-columns: repeat(6, minmax(0, 1fr));
		gap: 0.5rem;
		align-items: end;
	}
	.field-col {
		grid-column: span 3;
	}
	.target-col {
		grid-column: span 3;
	}
	.num-field {
		grid-column: span 2;
	}
	.field-col,
	.num-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.73rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		min-width: 0;
	}
	.field-col span,
	.num-field span {
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.editor-filters {
		padding-top: 0.55rem;
		border-top: 1px dashed var(--color-border);
	}
	.mono {
		font-family: var(--font-mono);
	}

	@media (max-width: 640px) {
		.field-col,
		.target-col {
			grid-column: 1 / -1;
		}
	}
</style>
