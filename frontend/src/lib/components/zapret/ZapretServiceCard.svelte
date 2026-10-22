<script lang="ts">
	import { m } from '$lib/i18n';
	import type { ZapretStatus } from '$lib/types';
	import { IconButton } from '$lib/components/ui';
	import { ChevronRight } from 'lucide-svelte';
	import ZapretStatusToggle from './ZapretStatusToggle.svelte';

	interface Props {
		status: ZapretStatus;
		busy?: boolean;
		oncontrol: (action: 'start' | 'stop') => void;
		onopen: () => void;
	}

	let { status, busy = false, oncontrol, onopen }: Props = $props();
	let title = $derived(
		status.flavor === 'zapret' ? 'Zapret' : status.flavor === 'zapret2' ? 'Zapret2' : 'Zapret'
	);
	let subtitle = $derived(
		status.lastError || (status.base
			? m.zapret_service_subtitle({ base: status.base })
			: m.zapret_service_subtitle_without_path())
	);
</script>

<section class="zapret-card" aria-label={m.zapret_service_aria()}>
	<ZapretStatusToggle status={status} {busy} {oncontrol} />
	<div class="identity">
		<strong>{title}</strong>
		<span class="subtitle" class:error={!!status.lastError} title={status.lastError || undefined}>{subtitle}</span>
	</div>
	<IconButton
		ariaLabel={m.zapret_service_open_settings_aria()}
		title={m.zapret_service_open_settings_title()}
		size="md"
		onclick={onopen}
	>
		<ChevronRight aria-hidden="true" />
	</IconButton>
</section>

<style>
	.zapret-card {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.625rem;
		padding: 0.75rem 0.875rem;
		margin: 0 0 1rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
	}
	.identity {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		min-width: 0;
	}
	.identity strong {
		color: var(--text-primary);
		font-size: 0.875rem;
		font-weight: 600;
	}
	.subtitle {
		color: var(--text-muted);
		font-size: 0.75rem;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.subtitle.error { color: var(--color-error); }
</style>
