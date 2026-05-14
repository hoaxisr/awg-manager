<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { saveStatus } from '$lib/stores/saveStatus';

	let unsub: () => void;
	onMount(() => {
		unsub = saveStatus.subscribe(() => {});
	});
	onDestroy(() => unsub?.());

	let s = $derived($saveStatus.data);
	let coordinatorState = $derived(s?.state ?? 'idle');
	let pending = $derived(s?.pendingCount ?? 0);
	let lastError = $derived(s?.lastError ?? '');

	let rawVisible = $derived(coordinatorState !== 'idle');

	let label = $derived.by(() => {
		if (coordinatorState === 'pending') return pending > 0 ? `Сохранение (${pending})` : 'Сохранение';
		if (coordinatorState === 'saving') return 'Сохранение...';
		if (coordinatorState === 'error') return 'Ошибка сохранения';
		if (coordinatorState === 'failed') return 'Сохранение не удалось';
		return '';
	});

	let tone = $derived.by(() => {
		if (coordinatorState === 'pending' || coordinatorState === 'saving') return 'pending';
		if (coordinatorState === 'error' || coordinatorState === 'failed') return 'error';
		return 'pending';
	});

	/** Плашка в шапке: слот фиксированной ширины; текст без мигания при серии запросов. */
	const HIDE_AFTER_IDLE_MS = 520;
	const LABEL_DEBOUNCE_MS = 200;

	let stickyActive = $state(false);
	let displayLabel = $state('');
	let displayTone = $state('pending');
	let displayTitle = $state('');

	let hideTimer: ReturnType<typeof setTimeout> | null = null;
	let labelTimer: ReturnType<typeof setTimeout> | null = null;

	function applyLabel(L: string, T: string, err: string) {
		displayLabel = L;
		displayTone = T === 'error' ? 'error' : 'pending';
		displayTitle = err || L;
	}

	$effect(() => {
		const alive = rawVisible;
		const L = label;
		const T = tone;
		const err = lastError;

		if (alive) {
			if (hideTimer) {
				clearTimeout(hideTimer);
				hideTimer = null;
			}
			stickyActive = true;

			const nextTone = T === 'error' ? 'error' : 'pending';
			const nextTitle = err || L;

			if (displayLabel === '') {
				if (labelTimer) {
					clearTimeout(labelTimer);
					labelTimer = null;
				}
				applyLabel(L, T, err);
			} else if (L === displayLabel && nextTone === displayTone && nextTitle === displayTitle) {
				// тот же снимок после опроса — не сбрасываем debounce
			} else {
				if (labelTimer) clearTimeout(labelTimer);
				labelTimer = setTimeout(() => {
					applyLabel(L, T, err);
					labelTimer = null;
				}, LABEL_DEBOUNCE_MS);
			}
		} else {
			if (labelTimer) {
				clearTimeout(labelTimer);
				labelTimer = null;
			}
			if (hideTimer) clearTimeout(hideTimer);
			hideTimer = setTimeout(() => {
				stickyActive = false;
				displayLabel = '';
				displayTitle = '';
				hideTimer = null;
			}, HIDE_AFTER_IDLE_MS);
		}

		return () => {
			if (hideTimer) {
				clearTimeout(hideTimer);
				hideTimer = null;
			}
			if (labelTimer) {
				clearTimeout(labelTimer);
				labelTimer = null;
			}
		};
	});
</script>

<span class="save-status-slot" aria-live="polite">
	{#if stickyActive}
		<span class="indicator indicator-{displayTone}" title={displayTitle}>{displayLabel}</span>
	{:else}
		<span class="save-status-placeholder" aria-hidden="true"></span>
	{/if}
</span>

<style>
	.save-status-slot {
		display: inline-flex;
		align-items: center;
		justify-content: flex-start;
		flex-shrink: 0;
		min-height: 1.375rem;
		box-sizing: border-box;
	}

	.save-status-placeholder {
		display: block;
		width: 100%;
		min-height: inherit;
	}

	.indicator {
		display: block;
		width: 100%;
		font-size: 0.75rem;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		box-sizing: border-box;
	}

	.indicator-pending {
		background: rgba(122, 162, 247, 0.1);
		color: var(--accent);
	}

	.indicator-error {
		background: rgba(239, 68, 68, 0.1);
		color: var(--error);
	}
</style>
