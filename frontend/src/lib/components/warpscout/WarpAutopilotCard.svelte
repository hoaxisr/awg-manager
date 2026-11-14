<script lang="ts">
	import { Plus, Save, RotateCcw } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button, ConfirmModal, SegmentedControl } from '$lib/components/ui';
	import type { SegmentedOption } from '$lib/components/ui';
	import type {
		WarpConfig,
		WarpProfileMode,
		WarpProfileStats,
		WarpSwapEvent,
		WarpTunnelRef,
		WarpWatchStatus,
	} from '$lib/types/warpscout';
	import WarpProfileCard from './WarpProfileCard.svelte';
	import WarpSwapHistory from './WarpSwapHistory.svelte';
	import { newProfile, setProfileEnabled, setProfileMode } from './warpscoutUi';

	interface Props {
		config: WarpConfig;
		targets: WarpTunnelRef[];
		watch: WarpWatchStatus[];
		stats?: WarpProfileStats[];
		history: WarpSwapEvent[];
		dirty: boolean;
		saving: boolean;
		onsave: () => void | Promise<void>;
		onreset: () => void;
		onresetstats?: (profileId: string) => void | Promise<void>;
		onclearhistory?: () => void | Promise<void>;
	}

	let {
		config = $bindable(),
		targets,
		watch,
		stats = [],
		history,
		dirty,
		saving,
		onsave,
		onreset,
		onresetstats,
		onclearhistory,
	}: Props = $props();

	const modeOptions = $derived<SegmentedOption<WarpProfileMode>[]>([
		{ value: 'single', label: m.warpscout_mode_single() },
		{ value: 'multi', label: m.warpscout_mode_multi() },
	]);

	let deleteId = $state<string | null>(null);
	let resetStatsId = $state<string | null>(null);
	let resettingStats = $state(false);
	let confirmClearHistory = $state(false);
	let clearingHistory = $state(false);

	const watchById = $derived(new Map((watch ?? []).map((w) => [w.profileId, w])));
	const statsById = $derived(new Map((stats ?? []).map((s) => [s.profileId, s])));

	async function confirmResetStats(): Promise<void> {
		if (!resetStatsId || !onresetstats) {
			resetStatsId = null;
			return;
		}
		resettingStats = true;
		try {
			await onresetstats(resetStatsId);
		} finally {
			resettingStats = false;
			resetStatsId = null;
		}
	}

	async function doClearHistory(): Promise<void> {
		if (!onclearhistory) {
			confirmClearHistory = false;
			return;
		}
		clearingHistory = true;
		try {
			await onclearhistory();
		} finally {
			clearingHistory = false;
			confirmClearHistory = false;
		}
	}

	function addProfile(): void {
		config.profiles.push(newProfile(config.profiles.length + 1));
	}

	function removeProfile(id: string): void {
		config.profiles = config.profiles.filter((p) => p.id !== id);
		if (config.activeProfileId === id) config.activeProfileId = undefined;
		deleteId = null;
	}

	const deleteName = $derived(config.profiles.find((p) => p.id === deleteId)?.name ?? '');
	const resetStatsName = $derived(config.profiles.find((p) => p.id === resetStatsId)?.name ?? '');
</script>

<section class="auto-card">
	<!-- Шапка автопилота -->
	<div class="auto-head">
		<div class="title-group">
			<h3>{m.warpscout_autopilot_title()}</h3>
		</div>
		<div class="head-controls">
			<div class="mode-seg">
				<SegmentedControl
					value={config.profileMode}
					options={modeOptions}
					ariaLabel={m.warpscout_profile_mode_aria()}
					onchange={(mode) => setProfileMode(config, mode)}
				/>
			</div>
			<Button size="sm" variant="secondary" onclick={addProfile}>
				{#snippet iconBefore()}<Plus size={13} />{/snippet}
				{m.warpscout_add_profile()}
			</Button>
		</div>
	</div>

	<!-- Список профилей с акцентом на живую телеметрию -->
	<div class="profiles-body">
		{#if config.profiles.length === 0 && !dirty}
			<div class="empty-bar">
				<p class="note">{m.warpscout_empty_profiles()}</p>
				<Button size="sm" variant="primary" onclick={addProfile}>
					{#snippet iconBefore()}<Plus size={13} />{/snippet}
					{m.warpscout_create_profile()}
				</Button>
			</div>
		{:else}
			{#if config.profiles.length === 0}
				<p class="note">{m.warpscout_all_profiles_removed()}</p>
			{/if}

			{#each config.profiles as p, i (p.id)}
				<WarpProfileCard
					bind:profile={config.profiles[i]}
					single={config.profileMode === 'single'}
					{targets}
					watch={watchById.get(p.id)}
					stats={statsById.get(p.id)}
					idPrefix={`profile-${i}`}
					onenable={(on) => setProfileEnabled(config, p.id, on)}
					ondelete={() => (deleteId = p.id)}
					onresetstats={onresetstats ? () => (resetStatsId = p.id) : undefined}
				/>
			{/each}

			{#if dirty}
				<div class="save-bar">
					<span class="dirty-msg">{m.warpscout_unsaved_changes()}</span>
					<div class="save-actions">
						<Button size="sm" variant="ghost" onclick={onreset} disabled={saving}>
							{#snippet iconBefore()}<RotateCcw size={13} />{/snippet}
							{m.warpscout_discard_btn()}
						</Button>
						<Button size="sm" variant="primary" loading={saving} onclick={onsave}>
							{#snippet iconBefore()}<Save size={13} />{/snippet}
							{m.common_save()}
						</Button>
					</div>
				</div>
			{/if}
		{/if}
	</div>

	<WarpSwapHistory
		{history}
		onclear={onclearhistory ? () => (confirmClearHistory = true) : undefined}
	/>
</section>

<ConfirmModal
	open={deleteId !== null}
	title={m.warpscout_confirm_del_profile_title()}
	message={m.warpscout_confirm_del_profile_msg({ name: deleteName })}
	confirmLabel={m.common_delete()}
	variant="danger"
	onConfirm={() => {
		if (deleteId) removeProfile(deleteId);
	}}
	onClose={() => (deleteId = null)}
/>

<ConfirmModal
	open={resetStatsId !== null}
	title={m.warpscout_confirm_reset_stats_title()}
	message={m.warpscout_confirm_reset_stats_msg({ name: resetStatsName })}
	secondary={m.warpscout_confirm_reset_stats_sec()}
	confirmLabel={m.warpscout_reset_btn()}
	variant="danger"
	busy={resettingStats}
	onConfirm={confirmResetStats}
	onClose={() => (resetStatsId = null)}
/>

<ConfirmModal
	open={confirmClearHistory}
	title={m.warpscout_confirm_clear_history_title()}
	message={m.warpscout_confirm_clear_history_msg({ count: history.length })}
	secondary={m.warpscout_confirm_clear_history_sec()}
	confirmLabel={m.warpscout_clear_history_confirm()}
	variant="danger"
	busy={clearingHistory}
	onConfirm={doClearHistory}
	onClose={() => (confirmClearHistory = false)}
/>

<style>
	.auto-card {
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		box-shadow: var(--shadow);
		overflow: hidden;
		display: flex;
		flex-direction: column;
	}
	.auto-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.85rem;
		padding: 0.6rem 0.85rem;
		border-bottom: 1px solid var(--color-border);
		flex-wrap: wrap;
	}
	.title-group {
		display: flex;
		align-items: baseline;
		gap: 0.2rem 0.6rem;
		flex-wrap: wrap;
		min-width: 0;
	}
	.auto-head h3 {
		margin: 0;
		font-size: 0.92rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.head-controls {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-left: auto;
		flex-wrap: wrap;
	}
	.mode-seg {
		flex-shrink: 0;
	}
	.profiles-body {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		padding: 0.65rem 0.85rem;
	}
	.empty-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.6rem 1rem;
		padding: 0.55rem 0.75rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--color-bg-primary) 65%, transparent);
		flex-wrap: wrap;
	}
	.save-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.85rem;
		padding: 0.45rem 0.7rem;
		border-radius: var(--radius-sm);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		flex-wrap: wrap;
	}
	.dirty-msg {
		font-size: 0.76rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}
	.save-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		margin-left: auto;
	}
	.note {
		margin: 0;
		font-size: 0.78rem;
		color: var(--color-text-secondary);
	}

	@media (max-width: 640px) {
		.head-controls {
			width: 100%;
			justify-content: space-between;
			margin-left: 0;
		}
	}
</style>
