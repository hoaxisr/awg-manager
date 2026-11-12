<script lang="ts">
	import type { Snippet } from 'svelte';
	import { m } from '$lib/i18n';
	import { SegmentedControl } from '$lib/components/ui';
	import type { SegmentedOption } from '$lib/components/ui';
	import type { WarpFilters, WarpProtocol } from '$lib/types/warpscout';
	import ChipCodesEditor from './ChipCodesEditor.svelte';
	import { QUICK_COLOS } from './warpscoutUi';

	interface Props {
		filters: WarpFilters;
		disabled?: boolean;
		hideProtocol?: boolean;
		idPrefix: string;
		actions?: Snippet;
	}

	let {
		filters = $bindable(),
		disabled = false,
		hideProtocol = false,
		idPrefix,
		actions,
	}: Props = $props();

	const protocolOptions = $derived<SegmentedOption<WarpProtocol>[]>([
		{ value: 'awg', label: m.warpscout_proto_awg_full() },
		{ value: 'wg', label: 'WireGuard' },
	]);
</script>

<div class="filters-root">
	<div class="filters-grid" class:no-proto={hideProtocol}>
		{#if !hideProtocol}
			<div class="field field-proto">
				<div class="field-head">
					<span class="ws-label">{m.warpscout_proto_label()}</span>
				</div>
				<div class="seg-wrap">
					<SegmentedControl
						value={filters.protocol}
						options={protocolOptions}
						ariaLabel={m.warpscout_proto_label()}
						{disabled}
						onchange={(v) => (filters.protocol = v)}
					/>
				</div>
			</div>
		{/if}

		<div class="field field-iata">
			<ChipCodesEditor
				label={m.warpscout_exclude_colos()}
				values={filters.excludeColos ?? []}
				codeLength={3}
				quick={QUICK_COLOS}
				placeholder={m.warpscout_iata_placeholder()}
				emptyLabel={m.warpscout_empty_none()}
				ariaLabel={m.warpscout_exclude_colos_aria()}
				{disabled}
				onchange={(next) => (filters.excludeColos = next)}
			/>
		</div>

		<div class="field field-iso">
			<ChipCodesEditor
				label={m.warpscout_allowed_countries()}
				values={filters.allowedCountries ?? []}
				codeLength={2}
				placeholder="FI, DE…"
				emptyLabel={m.warpscout_empty_any()}
				showFlags
				ariaLabel={m.warpscout_allowed_countries_aria()}
				{disabled}
				onchange={(next) => (filters.allowedCountries = next)}
			/>
		</div>

		<div class="field field-rtt">
			<div class="field-head">
				<label class="ws-label" for={`${idPrefix}-rtt`}>{m.warpscout_max_rtt()}</label>
				<span class="hint" title={m.warpscout_max_rtt_hint()}>0 = ∞</span>
			</div>
			<input
				id={`${idPrefix}-rtt`}
				class="num"
				type="number"
				min="0"
				max="5000"
				step="10"
				placeholder="0"
				title={m.warpscout_max_rtt_hint()}
				bind:value={filters.maxRttMs}
				{disabled}
			/>
		</div>
	</div>

	<div class="filters-bar">
		<label class="check">
			<input type="checkbox" bind:checked={filters.requireTelegram} {disabled} />
			<span>{m.warpscout_require_tg()}</span>
		</label>
		{#if actions}
			<div class="bar-actions">
				{@render actions()}
			</div>
		{/if}
	</div>
</div>

<style>
	.filters-root {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.filters-grid {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		gap: 0.5rem 0.65rem;
		align-items: start;
	}
	.filters-grid:not(.no-proto) .field-proto {
		order: 1;
	}
	.filters-grid:not(.no-proto) .field-rtt {
		order: 2;
	}
	.filters-grid:not(.no-proto) .field-iata {
		order: 3;
		grid-column: 1 / -1;
	}
	.filters-grid:not(.no-proto) .field-iso {
		order: 4;
		grid-column: 1 / -1;
	}
	.filters-grid.no-proto {
		grid-template-columns: minmax(11rem, 1.45fr) minmax(7.5rem, 0.95fr) 7.25rem;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		min-width: 0;
	}
	.field-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.25rem;
		flex-wrap: nowrap;
		min-height: 1.125rem;
	}
	.seg-wrap {
		align-self: flex-start;
		max-width: 100%;
	}
	.ws-label {
		font-size: 0.73rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		line-height: 1.2;
		white-space: nowrap;
	}
	.hint {
		font-size: 0.68rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono);
		line-height: 1.2;
		white-space: nowrap;
		flex-shrink: 0;
	}
	.num {
		width: 100%;
		height: 32px;
		box-sizing: border-box;
		padding: 0.25rem 0.5rem;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
	}
	.filters-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 0.75rem;
		flex-wrap: wrap;
	}
	.bar-actions {
		display: flex;
		align-items: center;
		gap: 0.55rem;
		margin-left: auto;
		flex-wrap: wrap;
	}
	.check {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.76rem;
		color: var(--color-text-secondary);
		cursor: pointer;
		user-select: none;
	}
	.check:hover {
		color: var(--color-text-primary);
	}

	@media (max-width: 820px) {
		.filters-grid.no-proto {
			grid-template-columns: minmax(0, 1fr) 8rem;
		}
		.filters-grid.no-proto .field-iata {
			grid-column: 1 / -1;
		}
	}

	@media (max-width: 640px) {
		.filters-grid,
		.filters-grid.no-proto {
			grid-template-columns: 1fr;
			gap: 0.5rem;
		}
		.filters-grid:not(.no-proto) .field-proto,
		.filters-grid:not(.no-proto) .field-iata,
		.filters-grid:not(.no-proto) .field-iso,
		.filters-grid:not(.no-proto) .field-rtt,
		.filters-grid.no-proto .field-iata {
			order: unset;
			grid-column: auto;
		}
		.seg-wrap {
			align-self: stretch;
			width: 100%;
		}
		.bar-actions {
			width: 100%;
			justify-content: space-between;
			margin-left: 0;
		}
	}
</style>
