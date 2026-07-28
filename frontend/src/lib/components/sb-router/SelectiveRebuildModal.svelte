<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import type { SelectiveDomainMatcherRecord, SelectiveProgress } from '$lib/types';
	import SelectiveMatcherTable from './SelectiveMatcherTable.svelte';

	interface Props {
		open: boolean;
		progress: SelectiveProgress | null;
		/** Свернуть окно, rebuild продолжается в фоне. */
		onMinimize: () => void;
		/** Закрыть после завершения и сбросить прогресс. */
		onDismiss: () => void;
	}

	let { open, progress, onMinimize, onDismiss }: Props = $props();

	const steps = [
		{ key: 'collecting', label: 'Сбор правил' },
		{ key: 'resolving', label: 'DNS-резолв' },
		{ key: 'populating', label: 'Активация ipset' },
		{ key: 'done', label: 'Готово' },
	] as const;

	const PAGE = 100;

	let doneMatchers = $state<SelectiveDomainMatcherRecord[]>([]);
	let doneTotal = $state(0);
	let doneLoading = $state(false);
	let doneLoadError = $state('');
	let doneMatchersLoaded = $state(false);

	function stepState(key: string, p: SelectiveProgress | null): 'pending' | 'active' | 'done' | 'error' {
		if (!p) return 'pending';
		if (p.phase === 'error') {
			const order = ['collecting', 'resolving', 'populating', 'done'];
			const idx = order.indexOf(key);
			const cur = order.indexOf('populating');
			if (idx < cur) return 'done';
			if (idx === cur) return 'error';
			return 'pending';
		}
		const order = ['collecting', 'resolving', 'populating', 'done'];
		const idx = order.indexOf(key);
		const cur = order.indexOf(p.phase);
		if (idx < cur) return 'done';
		if (idx === cur) return 'active';
		return 'pending';
	}

	// With the streaming pipeline the total grows while collection is still
	// running, so raw done/total can move backwards. Render a per-run
	// monotonic percentage instead: clamp to the maximum seen, resetting when
	// a fresh run starts (transition from idle/terminal to an active phase).
	let maxPct = $state(0);
	let prevPhase = '';

	$effect(() => {
		const p = progress;
		untrack(() => {
			const phase = p?.phase ?? '';
			const active = phase !== '' && phase !== 'done' && phase !== 'error';
			const wasIdle = prevPhase === '' || prevPhase === 'done' || prevPhase === 'error';
			if (!p) {
				maxPct = 0;
			} else if (active && wasIdle) {
				maxPct = 0; // new run
				cancelPending = false; // свежий прогон — кнопка отмены снова доступна
				cancelError = '';
			}
			if (p && active && p.total > 0) {
				const raw = Math.min(100, Math.round((p.current / p.total) * 100));
				if (raw > maxPct) maxPct = raw;
			}
			prevPhase = phase;
		});
	});

	const pct = $derived(maxPct);

	// While collection still streams, the total is a moving lower bound —
	// mark it with a tilde in the counter.
	const totalIsGrowing = $derived(progress?.phase === 'collecting');

	const finished = $derived(progress?.phase === 'done' || progress?.phase === 'error');

	// Отмена пересборки: кнопка видна, пока идёт активная фаза. После
	// успешного POST кнопка остаётся заблокированной («Отмена…») до прихода
	// терминального SSE-события (phase=error, «пересборка отменена
	// пользователем») — оно и закрывает цикл.
	let cancelPending = $state(false);
	let cancelError = $state('');

	async function handleCancelRebuild() {
		cancelPending = true;
		cancelError = '';
		try {
			await api.singboxRouterSelectiveRebuildCancel();
		} catch (e) {
			cancelPending = false;
			cancelError = e instanceof Error ? e.message : String(e);
		}
	}

	function handleBackdrop() {
		if (finished) onDismiss();
		else onMinimize();
	}

	function handleCloseButton() {
		if (finished) onDismiss();
		else onMinimize();
	}

	async function loadDoneMatchers(offset: number) {
		doneLoading = true;
		doneLoadError = '';
		try {
			const res = await api.singboxRouterSelectiveSnapshotMatchers(offset, PAGE);
			if (offset === 0) {
				doneMatchers = res.matchers;
			} else {
				doneMatchers = [...doneMatchers, ...res.matchers];
			}
			doneTotal = res.total;
		} catch (e) {
			doneLoadError = e instanceof Error ? e.message : String(e);
		} finally {
			doneLoading = false;
		}
	}

	function loadMoreDone() {
		if (!doneLoading && doneMatchers.length < doneTotal) {
			void loadDoneMatchers(doneMatchers.length);
		}
	}

	$effect(() => {
		if (progress?.phase === 'done' && open && !doneMatchersLoaded) {
			doneMatchersLoaded = true;
			void loadDoneMatchers(0);
		}
		if (progress?.phase !== 'done') {
			doneMatchersLoaded = false;
			doneMatchers = [];
			doneTotal = 0;
		}
	});
</script>

{#if open}
	<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
	<div class="overlay" role="presentation" onclick={handleBackdrop}>
		<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
		<div
			class="modal"
			role="dialog"
			aria-modal="true"
			aria-labelledby="sel-rebuild-title"
			tabindex="-1"
			onclick={(e) => e.stopPropagation()}
		>
			<header class="modal-head">
				<h2 id="sel-rebuild-title">Обновление ipset</h2>
				<button type="button" class="close" aria-label="Закрыть" onclick={handleCloseButton}>×</button>
			</header>

			<div class="steps">
				{#each steps as step (step.key)}
					{@const st = stepState(step.key, progress)}
					<div class="step" data-state={st}>
						<span class="step-icon">
							{#if st === 'done'}✓{:else if st === 'error'}✕{:else if st === 'active'}…{:else}○{/if}
						</span>
						<span class="step-label">{step.label}</span>
					</div>
				{/each}
			</div>

			{#if progress}
				<p class="msg">{progress.message}</p>
				{#if progress.total > 0 && progress.phase !== 'done' && progress.phase !== 'error'}
					<div class="bar-wrap">
						<div class="bar" style="width: {pct}%"></div>
					</div>
					<p class="counter">{progress.current} / {totalIsGrowing ? '~' : ''}{progress.total}</p>
				{/if}
				{#if progress.matcher && progress.phase !== 'done'}
					<p class="detail mono">
						{progress.matcher}
						{#if progress.queryHost}
							→ {progress.queryHost}
						{/if}
					</p>
				{/if}
				{#if progress.phase === 'error'}
					<p class="err">Ошибка при обновлении ipset. Проверьте логи awg-manager.</p>
				{/if}
				{#if progress.phase === 'done'}
					<details class="done-block" open>
						<summary>Доменные правила ({doneTotal || progress.current})</summary>
						<SelectiveMatcherTable
							matchers={doneMatchers}
							total={doneTotal}
							loading={doneLoading}
							loadError={doneLoadError}
							showLoadMore={doneMatchers.length < doneTotal}
							onLoadMore={loadMoreDone}
						/>
					</details>
				{/if}
			{:else}
				<p class="msg">Ожидание…</p>
			{/if}

			<footer class="modal-foot">
				{#if cancelError}
					<span class="err cancel-err">{cancelError}</span>
				{/if}
				{#if progress && !finished}
					<button
						type="button"
						class="btn btn-danger"
						disabled={cancelPending}
						onclick={handleCancelRebuild}
					>
						{cancelPending ? 'Отмена…' : 'Отменить пересборку'}
					</button>
				{/if}
				<button type="button" class="btn" onclick={handleCloseButton}>
					{finished ? 'Закрыть' : 'Свернуть'}
				</button>
			</footer>
		</div>
	</div>
{/if}

<style>
	.overlay {
		position: fixed;
		inset: 0;
		background: rgba(0, 0, 0, 0.55);
		z-index: 1000;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 16px;
	}
	.modal {
		background: var(--color-bg-secondary);
		border: 1px solid var(--border, #333);
		border-radius: 8px;
		max-width: 520px;
		width: 100%;
		max-height: 90vh;
		overflow: auto;
		padding: 16px;
	}
	.modal-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 12px;
	}
	.modal-head h2 {
		margin: 0;
		font-size: 16px;
	}
	.close {
		background: none;
		border: none;
		font-size: 22px;
		cursor: pointer;
		color: inherit;
		line-height: 1;
	}
	.steps {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-bottom: 12px;
	}
	.step {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 13px;
		opacity: 0.5;
	}
	.step[data-state='active'] {
		opacity: 1;
		font-weight: 500;
	}
	.step[data-state='done'] {
		opacity: 0.85;
		color: var(--color-success);
	}
	.step[data-state='error'] {
		opacity: 1;
		color: var(--color-error);
	}
	.step-icon {
		width: 1.2em;
		text-align: center;
	}
	.msg {
		font-size: 13px;
		margin: 0 0 8px;
	}
	.bar-wrap {
		height: 4px;
		background: var(--border, #333);
		border-radius: 2px;
		overflow: hidden;
		margin-bottom: 4px;
	}
	.bar {
		height: 100%;
		background: var(--accent, #4a9eff);
		transition: width 0.2s;
	}
	.counter,
	.detail {
		font-size: 11px;
		color: var(--text-muted, #888);
		margin: 0 0 4px;
	}
	.mono {
		font-family: var(--font-mono, monospace);
	}
	.err {
		color: var(--color-error);
		font-size: 12px;
	}
	.done-block {
		margin-top: 10px;
		font-size: 12px;
	}
	.done-block summary {
		cursor: pointer;
		font-weight: 500;
		margin-bottom: 6px;
	}
	.modal-foot {
		margin-top: 16px;
		display: flex;
		justify-content: flex-end;
		align-items: center;
		gap: 8px;
	}
	.btn {
		padding: 6px 14px;
		border-radius: 4px;
		border: 1px solid var(--border, #444);
		background: transparent;
		color: inherit;
		cursor: pointer;
		font-size: 13px;
	}
	.btn-danger {
		border-color: var(--color-error-border);
		color: var(--color-error);
	}
	.btn-danger:disabled {
		opacity: 0.6;
		cursor: default;
	}
	.cancel-err {
		margin-right: auto;
		text-align: left;
	}
</style>
