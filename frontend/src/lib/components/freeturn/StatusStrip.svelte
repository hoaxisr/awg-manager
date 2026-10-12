<script lang="ts">
	import { m } from '$lib/i18n';
	import { Toggle, StatusDot } from '$lib/components/ui';
	import type { FreeTurnProcessStatus } from '$lib/types';
	import { formatUptime } from './uptime';

	interface Props {
		client?: FreeTurnProcessStatus;
		server?: FreeTurnProcessStatus;
		onToggleClient: (on: boolean) => void;
		onToggleServer: (on: boolean) => void;
	}

	let { client, server, onToggleClient, onToggleServer }: Props = $props();

	function meta(status?: FreeTurnProcessStatus): string {
		if (!status?.running) return m.proxy_ft_status_stopped();
		return [m.proxy_ft_status_running(), formatUptime(status.startedAt), status.pid ? `PID ${status.pid}` : '']
			.filter(Boolean)
			.join(' · ');
	}
</script>

<div class="ft-strip">
	{#each [{ id: 'client', title: m.proxy_ft_client, status: client, onToggle: onToggleClient }, { id: 'server', title: m.proxy_ft_server, status: server, onToggle: onToggleServer }] as p (p.id)}
		<div class="ft-strip-card" class:running={p.status?.running}>
			<StatusDot size="sm" variant={p.status?.running ? 'success' : 'muted'} pulse={p.status?.running} />
			<div class="ft-strip-text">
				<div class="ft-strip-title">{p.title()}</div>
				<div class="ft-strip-meta">{meta(p.status)}</div>
			</div>
			<Toggle
				checked={!!p.status?.running}
				onchange={p.onToggle}
				disabled={p.status ? !p.status.binaryPresent : false}
				controlled
				size="sm"
				label=""
				ariaLabel={m.proxy_ft_toggle_aria({ title: p.title() })}
			/>
		</div>
	{/each}
</div>

<style>
	.ft-strip {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
		margin-bottom: 1rem;
	}

	.ft-strip-card {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		padding: 0.625rem 0.875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
	}

	.ft-strip-card.running {
		border-color: var(--color-border-hover);
	}

	.ft-strip-text {
		flex: 1;
		min-width: 0;
	}

	.ft-strip-title {
		font-size: 0.8125rem;
		font-weight: 600;
	}

	.ft-strip-meta {
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
		font-family: var(--font-mono);
	}

	@media (max-width: 640px) {
		.ft-strip {
			grid-template-columns: 1fr;
			gap: 0.5rem;
		}
	}
</style>
