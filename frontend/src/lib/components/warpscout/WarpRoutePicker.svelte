<script lang="ts">
	import { Check, Route } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import type { WarpRouteRef } from '$lib/types/warpscout';
	import WarpChipPopover from './WarpChipPopover.svelte';
	import { routeChipText, routeDisplayLabel } from './warpscoutUi';

	interface Props {
		routes: WarpRouteRef[];
		defaultWan: string;
		/** Выбранный интерфейс; пусто — основной WAN. */
		selected: string;
		onselect: (ifname: string) => void | Promise<void>;
	}

	let { routes, defaultWan, selected, onselect }: Props = $props();

	const groups = $derived([
		{
			label: m.warpscout_route_group_wan(),
			items: routes.filter((r) => r.kind === 'wan'),
			down: (name: string) => m.warpscout_route_no_link({ name }),
		},
		{
			label: m.warpscout_route_group_tunnels(),
			items: routes.filter((r) => r.kind === 'tunnel'),
			down: (name: string) => m.warpscout_route_stopped({ name }),
		},
		{
			label: m.warpscout_route_group_warp(),
			items: routes.filter((r) => r.kind === 'warp'),
			down: (name: string) => m.warpscout_route_stopped({ name }),
		},
	]);
</script>

<WarpChipPopover
	title={m.warpscout_route_pop_title()}
	chipTitle={m.warpscout_route_saved({ label: routeDisplayLabel(routes, defaultWan, selected) })}
	width="20.5rem"
	alignRight
>
	{#snippet chip()}
		<Route size={13} class="chip-ic" />
		<span class="chip-dim">{m.warpscout_route_chip_label()}</span>
		<span class="mono chip-strong">{routeChipText(routes, defaultWan, selected)}</span>
	{/snippet}

	{#snippet children(close)}
		<p class="pop-hint">{m.warpscout_route_pop_hint()}</p>

		<div class="route-list">
			<button
				type="button"
				class="route-opt"
				class:selected={!selected}
				aria-pressed={!selected}
				onclick={() => {
					close();
					void onselect('');
				}}
			>
				<span class="route-dot ok"></span>
				<div class="route-opt-main">
					<span class="route-opt-title">{m.warpscout_route_auto_default()}</span>
					<span class="route-opt-sub">
						{defaultWan ? m.warpscout_route_pref_link({ name: defaultWan }) : m.warpscout_route_ndms_prio()}
					</span>
				</div>
				{#if !selected}<Check size={14} class="route-check" />{/if}
			</button>

			{#each groups as g (g.label)}
				{#if g.items.length > 0}
					<div class="route-group-label">{g.label}</div>
					{#each g.items as r (r.name)}
						<button
							type="button"
							class="route-opt"
							class:selected={selected === r.name}
							aria-pressed={selected === r.name}
							onclick={() => {
								close();
								void onselect(r.name);
							}}
						>
							<span class="route-dot" class:ok={r.up}></span>
							<div class="route-opt-main">
								<span class="route-opt-title">{r.label || r.name}</span>
								<span class="route-opt-sub mono">{r.up ? r.name : g.down(r.name)}</span>
							</div>
							{#if selected === r.name}<Check size={14} class="route-check" />{/if}
						</button>
					{/each}
				{/if}
			{/each}
		</div>
	{/snippet}
</WarpChipPopover>

<style>
	.route-list {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		max-height: 15rem;
		overflow-y: auto;
	}
	.route-group-label {
		font-size: 0.67rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		padding: 0.3rem 0.25rem 0.1rem;
	}
	.route-opt {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		width: 100%;
		padding: 0.4rem 0.55rem;
		border-radius: var(--radius-sm);
		border: 1px solid transparent;
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font: inherit;
		text-align: left;
		cursor: pointer;
		transition: border-color 0.12s ease, background 0.12s ease;
	}
	.route-opt:hover {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}
	.route-opt.selected {
		border-color: var(--color-accent-border);
		background: var(--color-accent-tint);
	}
	.route-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-text-muted);
	}
	.route-dot.ok {
		background: var(--color-success);
	}
	.route-opt-main {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
	}
	.route-opt-title {
		font-size: 0.77rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.route-opt-sub {
		font-size: 0.69rem;
		color: var(--color-text-muted);
	}
	.route-opt :global(.route-check) {
		color: var(--color-accent);
		flex-shrink: 0;
	}
	.mono {
		font-family: var(--font-mono);
	}
</style>
