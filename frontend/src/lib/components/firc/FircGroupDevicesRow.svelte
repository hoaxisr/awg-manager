<script lang="ts">
	import { m } from '$lib/i18n';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		entryKey: string;
		mode: 'all' | 'allow';
		isOn: boolean;
		disabled?: boolean;
		label: string;
		meta: string;
		title: string;
		online?: boolean;
		policyName?: string;
		policyLabel?: string;
		policyBadge?: string;
		policyBadgeVariant?: 'warn' | 'accent';
		lockTitle?: string;
		onset: (enabled: boolean) => void;
	}

	let {
		entryKey,
		mode,
		isOn,
		disabled = false,
		label,
		meta,
		title,
		online,
		policyName,
		policyLabel,
		policyBadge,
		policyBadgeVariant,
		lockTitle,
		onset,
	}: Props = $props();
</script>

<div
	class="dm-row"
	class:dm-row-excluded={mode === 'all' && !isOn}
	class:dm-row-selected={mode === 'allow' && isOn}
	class:dm-row-muted={mode === 'allow' && !isOn}
	class:dm-row-policy-locked={disabled}
	data-entry={entryKey}
	data-enabled={isOn ? 'true' : 'false'}
	data-policy-locked={disabled ? 'true' : undefined}
	data-policy={policyName || undefined}
	title={lockTitle || undefined}
>
	<button
		type="button"
		class="dm-row-trigger"
		{title}
		{disabled}
		onclick={() => onset(!isOn)}
	>
		{#if online !== undefined}
			<span
				class="dm-online-dot"
				class:dm-online={online}
				title={online ? m.firc_devices_online() : m.firc_devices_offline()}
			></span>
		{/if}
		<span class="dm-row-text">
			<span class="dm-row-name">{label}</span>
			{#if meta || policyLabel}
				<span class="dm-row-meta-row">
					{#if meta}
						<span class="dm-row-meta">{meta}</span>
					{/if}
					{#if policyLabel}
						<span class="dm-policy-tag">{policyLabel}</span>
					{/if}
				</span>
			{/if}
		</span>
	</button>
	<div class="dm-row-control">
		{#if policyBadge && policyBadgeVariant === 'warn'}
			<span class="dm-excl-pill" title={lockTitle || undefined}>{policyBadge}</span>
		{:else if policyBadge && policyBadgeVariant === 'accent'}
			<span class="dm-pol-pill-allow" title={lockTitle || undefined}>{policyBadge}</span>
		{:else if mode === 'all' && !isOn}
			<span class="dm-excl-pill">{m.firc_devices_badge_excluded()}</span>
		{/if}
		<Toggle
			size="sm"
			spinner="none"
			controlled
			checked={isOn}
			{disabled}
			ariaLabel={label}
			onchange={(next) => onset(next)}
		/>
	</div>
</div>

<style>
	.dm-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		min-width: 0;
		padding: 6px 9px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		transition:
			background var(--t-fast),
			border-color var(--t-fast),
			opacity var(--t-fast);
	}

	.dm-row:hover {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}

	.dm-row.dm-row-selected {
		background: color-mix(in srgb, var(--color-accent-tint) 65%, var(--color-bg-primary));
		border-color: var(--color-accent-border);
	}

	.dm-row.dm-row-excluded {
		background: color-mix(in srgb, var(--color-warning-tint) 65%, var(--color-bg-primary));
		border-color: var(--color-warning-border);
		opacity: 0.78;
	}

	.dm-row.dm-row-muted {
		opacity: 0.72;
	}

	.dm-row.dm-row-muted:hover,
	.dm-row.dm-row-excluded:hover {
		opacity: 0.96;
	}

	.dm-row-trigger {
		flex: 1;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: 7px;
		border: 0;
		padding: 0;
		background: transparent;
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	.dm-row-trigger:disabled {
		cursor: default;
	}

	.dm-row.dm-row-policy-locked:hover {
		background: var(--color-bg-primary);
		border-color: var(--color-border);
	}

	.dm-row.dm-row-excluded.dm-row-policy-locked:hover {
		background: color-mix(in srgb, var(--color-warning-tint) 65%, var(--color-bg-primary));
		border-color: var(--color-warning-border);
	}

	.dm-row.dm-row-selected.dm-row-policy-locked:hover {
		background: color-mix(in srgb, var(--color-accent-tint) 65%, var(--color-bg-primary));
		border-color: var(--color-accent-border);
	}

	.dm-online-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-text-muted);
		opacity: 0.45;
	}

	.dm-online-dot.dm-online {
		background: var(--color-success);
		opacity: 1;
	}

	.dm-row-text {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.dm-row-name {
		font-size: 12.5px;
		font-weight: 500;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dm-row.dm-row-selected .dm-row-name {
		font-weight: 600;
	}

	.dm-row-meta-row {
		display: flex;
		align-items: center;
		gap: 5px;
		min-width: 0;
	}

	.dm-row-meta {
		min-width: 0;
		font-family: var(--font-mono);
		font-size: 10.5px;
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dm-policy-tag {
		flex-shrink: 0;
		max-width: 110px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: 9.5px;
		font-weight: 600;
		padding: 0 5px;
		border-radius: 4px;
		background: color-mix(in srgb, var(--color-accent-tint) 45%, transparent);
		border: 1px solid color-mix(in srgb, var(--color-accent-border) 60%, var(--color-border));
		color: var(--color-text-secondary);
	}

	.dm-row-control {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		flex-shrink: 0;
	}

	.dm-excl-pill {
		font-size: 10px;
		font-weight: 600;
		padding: 1px 6px;
		border-radius: var(--radius-pill);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
		white-space: nowrap;
	}

	.dm-pol-pill-allow {
		font-size: 10px;
		font-weight: 600;
		padding: 1px 6px;
		border-radius: var(--radius-pill);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
		white-space: nowrap;
	}
</style>
