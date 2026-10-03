<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import type { UsageLevel } from '$lib/types/usageLevel';
	import { usageLevelLabel } from '$lib/types/usageLevel';
	import { m } from '$lib/i18n';
	import { SlidersHorizontal, ChevronDown, Info, Check } from 'lucide-svelte';

	interface Props {
		value: UsageLevel;
		saving: boolean;
		onSelect: (level: UsageLevel) => void | Promise<void>;
		initialExpanded?: boolean;
		highlighted?: boolean;
	}

	let { value, saving, onSelect, initialExpanded = false, highlighted = false }: Props = $props();

	type LevelOption = {
		value: UsageLevel;
		title: string;
		summary: string;
		includes: string[];
	};

	// $derived: тексты пересчитываются при смене языка.
	const OPTIONS: LevelOption[] = $derived([
		{
			value: 'basic',
			title: usageLevelLabel('basic'),
			summary: m.settings_level_basic_summary(),
			includes: [
				m.settings_level_basic_include_awg(),
				m.settings_level_basic_include_system(),
				m.settings_level_basic_include_diagnostics(),
				m.settings_level_basic_include_routing(),
				m.settings_level_basic_include_system_card(),
			],
		},
		{
			value: 'advanced',
			title: usageLevelLabel('advanced'),
			summary: m.settings_level_advanced_summary(),
			includes: [
				m.settings_level_advanced_include_basic(),
				m.settings_level_advanced_include_singbox(),
				m.settings_level_advanced_include_servers(),
				m.settings_level_advanced_include_routing(),
				m.settings_level_advanced_include_terminal(),
				m.settings_level_advanced_include_monitoring(),
				m.settings_level_advanced_include_system_card(),
				m.settings_level_advanced_include_theme(),
				m.settings_level_advanced_include_compact(),
			],
		},
		{
			value: 'expert',
			title: usageLevelLabel('expert'),
			summary: m.settings_level_expert_summary(),
			includes: [
				m.settings_level_expert_include_advanced(),
				'HydraRoute Neo',
				'Sing-box Router',
				m.settings_level_expert_include_awg_check(),
				m.settings_level_expert_include_api_key(),
				m.settings_level_expert_include_system_card(),
			],
		},
	]);

	let infoFor = $state<UsageLevel | null>(null);
	const infoOpt = $derived(infoFor ? OPTIONS.find((o) => o.value === infoFor) : null);

	let expanded = $state(false);
	$effect(() => {
		expanded = initialExpanded;
	});

	function selectLevel(level: UsageLevel) {
		if (level === value || saving) return;
		void onSelect(level);
	}

	function openInfo(e: Event, level: UsageLevel) {
		e.stopPropagation();
		infoFor = level;
	}
</script>

<div class="settings-block">
	<div class="card" class:highlighted>
	<SettingsSectionLabel label={m.settings_general()} icon={SlidersHorizontal} tone="slate" header />
	<div class="setting-row level-header-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_level_title()}</span>
			<span class="setting-description">
				{m.settings_level_description()}
			</span>
		</div>
		<button
			type="button"
			class="level-expand-control"
			aria-expanded={expanded}
			aria-controls="usage-level-picker"
			aria-label={m.settings_level_toggle_aria()}
			onclick={() => (expanded = !expanded)}
		>
			<span class="current-level">{usageLevelLabel(value)}</span>
			<span class="chevron" class:open={expanded} aria-hidden="true"><ChevronDown size={14} strokeWidth={2} /></span>
		</button>
	</div>

	{#if expanded}
		<div id="usage-level-picker" class="level-picker">
			<div
				class="level-grid"
				role="radiogroup"
				aria-label={m.settings_level_title()}
				aria-busy={saving}
			>
				{#each OPTIONS as opt (opt.value)}
					{@const selected = value === opt.value}
					<button
						type="button"
						role="radio"
						aria-checked={selected}
						class="level-card"
						class:selected
						disabled={saving}
						onclick={() => selectLevel(opt.value)}
					>
						<span
							class="info-btn"
							role="button"
							tabindex="0"
							aria-label={m.settings_level_info_aria({ level: opt.title })}
							onclick={(e) => openInfo(e, opt.value)}
							onkeydown={(e) => {
								if (e.key === 'Enter' || e.key === ' ') {
									openInfo(e, opt.value);
								}
							}}
						>
							<Info size={12} strokeWidth={2} aria-hidden="true" />
						</span>

						<div class="level-title">{opt.title}</div>

						{#if selected}
							<span class="level-check" aria-hidden="true">
								<Check size={14} strokeWidth={2} />
							</span>
						{/if}
					</button>
				{/each}
			</div>
		</div>
	{/if}
	</div>
</div>

<Modal
	open={infoFor !== null}
	title={infoOpt ? m.settings_level_info_title({ level: infoOpt.title }) : ''}
	size="md"
	onclose={() => (infoFor = null)}
>
	{#if infoOpt}
		<div class="level-info-panel">
			<div class="level-info-summary">
				<span class="level-info-eyebrow">{m.settings_level_info_summary()}</span>
				<p>{infoOpt.summary}</p>
			</div>

			<div class="level-info-section">
				<h3>{m.settings_level_info_includes()}</h3>
				<ul class="level-info-list">
					{#each infoOpt.includes as item}
						<li class="level-info-item">
							<span class="level-info-bullet" aria-hidden="true">
								<Check size={14} strokeWidth={2} />
							</span>
							<span>{item}</span>
						</li>
					{/each}
				</ul>
			</div>
		</div>
	{/if}
</Modal>

<style>
	.level-header-row {
		align-items: center;
	}

	@media (max-width: 640px) {
		.level-header-row {
			flex-direction: row;
			align-items: center;
			flex-wrap: nowrap;
			gap: 0.75rem;
		}

		.level-header-row > *:first-child {
			flex: 1 1 auto;
			min-width: 0;
		}
	}

	.level-expand-control {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
		background: transparent;
		border: 0;
		padding: 0;
		margin: 0;
		color: var(--color-text-muted);
		font-size: 0.8125rem;
		cursor: pointer;
	}

	.level-expand-control:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
		border-radius: var(--radius-sm);
	}

	.current-level {
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 8rem;
	}

	.chevron {
		display: inline-flex;
		width: 14px;
		height: 14px;
		transition: transform var(--t-fast) ease;
	}

	.chevron.open {
		transform: rotate(180deg);
	}

	.level-picker {
		border-top: 1px solid var(--color-border);
	}

	.level-picker .level-grid {
		padding-top: 0.875rem;
	}

	.level-grid {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 0.5rem;
	}
	@media (max-width: 480px) {
		.level-grid {
			grid-template-columns: 1fr;
		}
	}

	.level-card {
		position: relative;
		text-align: center;
		padding: 0.625rem 0.5rem;
		background: var(--color-settings-control-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: inherit;
		font: inherit;
		cursor: pointer;
		transition:
			border-color var(--t-fast) ease,
			background var(--t-fast) ease;
	}
	.level-card:hover:not(:disabled):not(.selected) {
		background: var(--color-bg-hover);
		border-color: var(--color-border-strong);
	}
	.level-card:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}
	.level-card.selected {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.level-card:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.level-title {
		font-weight: 600;
		font-size: 0.875rem;
		padding: 0 1.25rem;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.level-check {
		position: absolute;
		top: 0.375rem;
		left: 0.375rem;
		width: 14px;
		height: 14px;
		color: var(--color-accent);
	}
	.info-btn {
		position: absolute;
		top: 0.375rem;
		right: 0.375rem;
		width: 14px;
		height: 14px;
		color: var(--color-text-muted);
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
	}
	.info-btn:hover {
		color: var(--color-text-primary);
	}
	.info-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}
	.level-info-panel {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.level-info-summary {
		padding: 0.875rem 1rem;
		background: var(--color-settings-control-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
	}

	.level-info-eyebrow {
		display: inline-flex;
		margin-bottom: 0.375rem;
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--color-text-muted);
	}

	.level-info-summary p {
		margin: 0;
		font-size: 1rem;
		line-height: 1.5;
		color: var(--color-text-primary);
	}

	.level-info-section h3 {
		margin: 0 0 0.75rem;
		font-size: 0.9375rem;
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.level-info-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.level-info-item {
		display: flex;
		align-items: flex-start;
		gap: 0.75rem;
		padding: 0.75rem 0.875rem;
		background: var(--color-settings-control-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		line-height: 1.45;
		color: var(--color-text-secondary);
	}

	.level-info-bullet {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.25rem;
		height: 1.25rem;
		flex: 0 0 1.25rem;
		margin-top: 0.0625rem;
		border-radius: 999px;
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}



	.card.highlighted {
		animation: usage-level-glow 2.8s ease-out forwards;
	}

	@keyframes usage-level-glow {
		0%   { box-shadow: none; }
		12%  { box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 55%, transparent), 0 0 18px 2px color-mix(in srgb, var(--color-accent) 22%, transparent); }
		30%  { box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 20%, transparent); }
		48%  { box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 40%, transparent), 0 0 14px 2px color-mix(in srgb, var(--color-accent) 15%, transparent); }
		65%  { box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 15%, transparent); }
		82%  { box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-accent) 22%, transparent), 0 0 8px 1px color-mix(in srgb, var(--color-accent) 10%, transparent); }
		100% { box-shadow: none; }
	}

	@media (max-width: 640px) {
		.level-header-row {
			flex-direction: column;
			align-items: stretch;
			flex-wrap: nowrap;
			gap: 0.625rem;
		}

		.level-header-row > *:first-child {
			width: 100%;
		}

		.level-expand-control {
			width: 100%;
			box-sizing: border-box;
			justify-content: space-between;
			padding: 0.45rem 0.625rem;
			border: 1px solid var(--color-border);
			border-radius: var(--radius-sm);
			background: var(--color-settings-control-bg);
		}

		.current-level {
			max-width: none;
		}
	}

	@media (min-width: 641px) {
		.level-header-row {
			display: grid;
			grid-template-columns: minmax(0, 1fr);
			align-items: stretch;
			gap: 0.75rem;
		}

		.level-header-row > *:first-child {
			width: 100%;
		}

		.level-expand-control {
			width: 100%;
			box-sizing: border-box;
			justify-content: space-between;
			padding: 0.45rem 0.625rem;
			border: 1px solid var(--color-border);
			border-radius: var(--radius-sm);
			background: var(--color-settings-control-bg);
		}

		.current-level {
			max-width: none;
		}
	}
</style>
