<script lang="ts">
	import { m } from '$lib/i18n';
	import { Toggle, Button, SegmentedControl } from '$lib/components/ui';
	import type { Settings } from '$lib/types';
	import { pageDraft } from './pageState.svelte';

	interface Props {
		settings: Settings;
		saving: boolean;
		onToggle: (enabled: boolean) => void;
		onSave: (dnsRoute: Settings['dnsRoute']) => void | Promise<void>;
	}

	let {
		settings,
		saving,
		onToggle,
		onSave,
	}: Props = $props();

	const mode = pageDraft('dns-mode', () => settings.dnsRoute.refreshMode || 'interval');
	const interval = pageDraft('dns-interval', () => settings.dnsRoute.refreshIntervalHours || 6);
	const dailyTime = pageDraft('dns-daily', () => settings.dnsRoute.refreshDailyTime || '03:00');

	const modeOptions = $derived([
		{ value: 'interval' as const, label: m.settings_dnsroute_mode_interval() },
		{ value: 'daily' as const, label: m.settings_dnsroute_mode_daily() },
	]);

	const settingsChanged = $derived(mode.dirty || (mode.value === 'interval' ? interval.dirty : dailyTime.dirty));

	// `settings` is not touched until the page saves it: after a failed save the
	// controls fall back to the last saved values instead of the rejected ones.
	async function handleSave() {
		await onSave({
			...settings.dnsRoute,
			refreshMode: mode.value,
			...(mode.value === 'interval'
				? { refreshIntervalHours: interval.value }
				: { refreshDailyTime: dailyTime.value }),
		});
		mode.reset();
		interval.reset();
		dailyTime.reset();
	}
</script>

<div class="setting-row toggle-inline-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_dnsroute_auto_label()}</span>
		<span class="setting-description">
			{m.settings_dnsroute_auto_description()}
		</span>
	</div>
	<Toggle checked={settings.dnsRoute.autoRefreshEnabled} controlled onchange={onToggle} disabled={saving} />
</div>

{#if settings.dnsRoute.autoRefreshEnabled}
	<div class="setting-sub">
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">{m.settings_dnsroute_mode_label()}</span>
				<span class="setting-description">
					{mode.value === 'interval' ? m.settings_dnsroute_interval_hint() : m.settings_dnsroute_daily_hint()}
				</span>
			</div>
			<div class="inline-form">
				<SegmentedControl
					value={mode.value}
					options={modeOptions}
					ariaLabel={m.settings_dnsroute_mode_label()}
					disabled={saving}
					onchange={(next) => (mode.value = next)}
				/>
				{#if mode.value === 'interval'}
					<div class="input-with-suffix">
						<input
							type="number"
							id="dnsRefreshInterval"
							bind:value={interval.value}
							min="1"
							max="48"
							disabled={saving}
						/>
						<span class="input-suffix">{m.settings_dnsroute_hours_suffix()}</span>
					</div>
				{:else}
					<input type="time" id="dnsRefreshTime" bind:value={dailyTime.value} disabled={saving} />
				{/if}
				{#if settingsChanged}
					<Button variant="primary" size="sm" onclick={handleSave} loading={saving}>
						{saving ? m.common_saving() : m.common_save()}
					</Button>
				{/if}
			</div>
		</div>
	</div>
{/if}
