<script lang="ts">
	import { Radar } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Badge, type BadgeSize, type BadgeVariant } from '$lib/components/ui';
	import type { WarpAutopilotInfo } from '$lib/types';

	interface Props {
		autopilot?: WarpAutopilotInfo | null;
		size?: BadgeSize;
	}

	let { autopilot, size = 'xs' }: Props = $props();

	function stateLabel(state: string): string {
		switch (state) {
			case 'ok':
				return m.tunnels_warp_state_ok();
			case 'idle':
				return m.tunnels_warp_state_idle();
			case 'swapping':
				return m.tunnels_warp_state_swapping();
			case 'degraded':
				return m.tunnels_warp_state_degraded();
			case 'backoff':
				return m.tunnels_warp_state_backoff();
			case 'engine_busy':
				return m.tunnels_warp_state_engine_busy();
			case 'engine_missing':
				return m.tunnels_warp_state_engine_missing();
			default:
				return state;
		}
	}

	const variant = $derived<BadgeVariant>(
		autopilot?.state === 'degraded' ||
			autopilot?.state === 'backoff' ||
			autopilot?.state === 'engine_missing' ||
			autopilot?.state === 'engine_busy'
			? 'warning'
			: 'accent',
	);

	const tooltip = $derived.by(() => {
		if (!autopilot) return '';
		const prof = autopilot.profileName?.trim();
		const head = prof ? m.tunnels_warp_autopilot_named({ name: prof }) : m.tunnels_warp_autopilot_head();
		const stateText = autopilot.message?.trim() || stateLabel(autopilot.state);
		return stateText ? `${head}: ${stateText}` : head;
	});
</script>

{#if autopilot}
	<a
		class="warp-autopilot-badge"
		href="/diagnostics?tab=warpscout"
		title={tooltip}
		onclick={(e) => e.stopPropagation()}
	>
		<Badge {size} {variant}>
			<Radar size={10} aria-hidden="true" />
			<span>{m.tunnels_warp_autopilot_badge()}</span>
		</Badge>
	</a>
{/if}

<style>
	.warp-autopilot-badge {
		display: inline-flex;
		align-items: center;
		flex-shrink: 0;
		text-decoration: none;
	}

	.warp-autopilot-badge:hover {
		filter: brightness(1.08);
	}
</style>
