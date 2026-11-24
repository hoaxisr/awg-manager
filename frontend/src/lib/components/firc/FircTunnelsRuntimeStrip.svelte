<script lang="ts">
	import { m } from '$lib/i18n';
	import { stripFircEmojis, type FircTunnel, type FircTunnelState } from '$lib/types/firc';
	import { Badge } from '$lib/components/ui';

	interface Props {
		tunnels: FircTunnel[];
		liveStateMap: Record<string, FircTunnelState>;
		activeRoutingEngine: string;
		daemonRunning: boolean;
		daemonDisabled: boolean;
	}

	let { tunnels, liveStateMap, activeRoutingEngine, daemonRunning, daemonDisabled }: Props = $props();

	function getInboundStatuses(tun: FircTunnel) {
		const st = liveStateMap[tun.id];
		const liveInbs = st?.inbounds ?? [];
		return (tun.inbounds ?? []).map((inb) => {
			const match = liveInbs.find((s) => s.port === inb.port);
			if (!tun.enable || !daemonRunning) {
				return {
					port: inb.port,
					state: 'off' as const,
					label: m.firc_inbound_off(),
					why: match?.why ?? '',
				};
			}
			const s = match?.state ?? '';
			if (s === 'up') {
				return { port: inb.port, state: 'up' as const, label: m.firc_inbound_up(), why: match?.why ?? '' };
			}
			if (s === 'no-node') {
				return { port: inb.port, state: 'no-node' as const, label: m.firc_inbound_no_node(), why: match?.why ?? '' };
			}
			if (s === 'busy') {
				return { port: inb.port, state: 'busy' as const, label: m.firc_inbound_busy(), why: match?.why ?? '' };
			}
			return { port: inb.port, state: 'off' as const, label: m.firc_inbound_off(), why: match?.why ?? '' };
		});
	}

	function getActiveNodeLabel(tun: FircTunnel): string | null {
		const st = liveStateMap[tun.id];
		if (!st) return null;
		if (st.active && st.active.length > 0) {
			const first = stripFircEmojis(st.active[0]);
			return st.active.length > 1 ? `${first} (+${st.active.length - 1})` : first;
		}
		const act = (st.nodes ?? []).filter((n) => n.state === 'active');
		if (act.length === 0) return null;
		const name = stripFircEmojis(act[0].name || act[0].key);
		return act.length > 1 ? `${name} (+${act.length - 1})` : name;
	}

	const runtimeOverview = $derived.by(() => {
		const activeNames = tunnels
			.map((t) => getActiveNodeLabel(t))
			.filter((name) => Boolean(name));
		const allInbs = tunnels.flatMap((t) => getInboundStatuses(t));
		const firstBad = allInbs.find((i) => i.state === 'no-node' || i.state === 'busy');
		const firstUp = allInbs.find((i) => i.state === 'up');
		const firstOff = allInbs[0];
		const inboundBadge = firstBad ?? firstUp ?? firstOff ?? null;
		const daemonLabel = daemonRunning
			? m.firc_daemon_running()
			: daemonDisabled
				? m.firc_daemon_disabled()
				: m.firc_daemon_stopped();
		return {
			activeNodeText: activeNames[0] ?? '—',
			inboundBadge,
			daemonLabel,
		};
	});
</script>

<div class="firc-runtime-strip" data-testid="firc-runtime-strip">
	<div class="firc-runtime-pill">
		<span class="firc-runtime-k">{m.firc_engine_label()}:</span>
		<Badge variant={activeRoutingEngine === 'FIRC' ? 'accent' : 'default'} size="xs">
			{activeRoutingEngine}
		</Badge>
	</div>
	<div class="firc-runtime-pill">
		<span class="firc-runtime-k">{m.firc_daemon_label()}:</span>
		<Badge
			variant={daemonRunning ? 'success' : daemonDisabled ? 'warning' : 'default'}
			size="xs"
		>
			{runtimeOverview.daemonLabel}
		</Badge>
	</div>
	<div class="firc-runtime-pill">
		<span class="firc-runtime-k">{m.firc_node_label()}:</span>
		<span class="firc-runtime-v mono">{runtimeOverview.activeNodeText}</span>
	</div>
	<div class="firc-runtime-pill">
		<span class="firc-runtime-k">{m.firc_inbound_label()}:</span>
		{#if runtimeOverview.inboundBadge}
			<Badge
				variant={runtimeOverview.inboundBadge.state === 'up'
					? 'success'
					: runtimeOverview.inboundBadge.state === 'no-node' ||
						  runtimeOverview.inboundBadge.state === 'busy'
						? 'error'
						: 'default'}
				size="xs"
			>
				:{runtimeOverview.inboundBadge.port} · {runtimeOverview.inboundBadge.label}
			</Badge>
		{:else}
			<span class="firc-runtime-v">{m.firc_inbound_none()}</span>
		{/if}
	</div>
</div>

<style>
	.firc-runtime-strip {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px 16px;
		padding: 10px 14px;
		border: 1px solid var(--color-border);
		border-radius: 10px;
		background: var(--color-bg-secondary);
		font-size: 12px;
	}

	.firc-runtime-pill {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}

	.firc-runtime-k {
		color: var(--color-text-muted);
	}

	.firc-runtime-v {
		color: var(--color-text-primary);
		font-weight: 500;
	}

	.mono {
		font-family: var(--font-mono);
	}
</style>
