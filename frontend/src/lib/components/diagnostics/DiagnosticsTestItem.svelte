<script lang="ts">
	import type { DiagTestEvent } from '$lib/types';
	import { slide } from 'svelte/transition';

	interface Props {
		test: DiagTestEvent;
		expandGeneration?: number;
		expandDirection?: boolean;
	}

	let { test, expandGeneration = 0, expandDirection = false }: Props = $props();
	// svelte-ignore state_referenced_locally — intentional: initial value from test.status, then user-controlled
	let expanded = $state(test.status === 'fail' || test.status === 'error');

	$effect(() => {
		if (expandGeneration > 0) {
			expanded = expandDirection;
		}
	});

	const icons: Record<string, string> = {
		pass: '\u2713',
		fail: '\u2717',
		skip: '\u2014',
		error: '!'
	};
</script>

<button
	class="test-item test-{test.status}"
	onclick={() => expanded = !expanded}
	transition:slide={{ duration: 200 }}
>
	<span class="test-icon">{icons[test.status] ?? '?'}</span>
	<span class="test-name">{test.description}</span>
	{#if test.detail}
		<span class="test-expand">{expanded ? '\u25BE' : '\u25B8'}</span>
	{/if}
</button>

{#if expanded && test.detail}
	<div class="test-detail" transition:slide={{ duration: 150 }}>
		{test.detail}
	</div>
{/if}

<style>
	.test-item {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 8px 12px;
		width: 100%;
		border: none;
		background: none;
		cursor: pointer;
		text-align: left;
		font-size: 14px;
		color: var(--text-primary);
		border-radius: 6px;
	}

	.test-item:hover {
		background: var(--bg-secondary);
	}

	.test-icon {
		width: 20px;
		height: 20px;
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		font-size: 12px;
		font-weight: 700;
		flex-shrink: 0;
	}

	.test-pass .test-icon {
		background: rgba(34, 197, 94, 0.15);
		color: #22c55e;
	}

	.test-fail .test-icon {
		background: rgba(239, 68, 68, 0.15);
		color: #ef4444;
	}

	.test-skip .test-icon {
		background: rgba(156, 163, 175, 0.15);
		color: #9ca3af;
	}

	.test-error .test-icon {
		background: rgba(234, 179, 8, 0.15);
		color: #eab308;
	}

	.test-name {
		flex: 1;
	}

	.test-expand {
		color: var(--text-tertiary);
		font-size: 12px;
	}

	.test-detail {
		padding: 4px 12px 8px 42px;
		font-size: 13px;
		color: var(--text-secondary);
		line-height: 1.4;
	}
</style>
