<script lang="ts">
	import { m } from '$lib/i18n';
	import type { Snippet } from 'svelte';
	import { formatAutolistDuration, formatAutolistTime } from './time';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import type { AutolistCandidate } from '$lib/types';
	import { Modal, Button } from '$lib/components/ui';
	import { RotateCw } from 'lucide-svelte';

	interface Props {
		open: boolean;
		/** Карточка для повторной проверки; null — модалка безработна. */
		candidate: AutolistCandidate | null;
		onclose: () => void;
		/** Кнопки родителя («В туннель»/«Убрать») в футере модалки. */
		actions?: Snippet;
		/** Детальная информация по проверке (общий блок с карточкой кандидата). */
		details?: Snippet<[AutolistCandidate, boolean]>;
	}

	let { open, candidate, onclose, actions, details }: Props = $props();

	type Phase = 'idle' | 'running' | 'done' | 'error';

	let phase = $state<Phase>('idle');
	let result = $state<AutolistCandidate | null>(null);
	let failure = $state('');

	// Открытие модалки — старт повторной проверки. Кандидат фиксируется на
	// момент открытия: параллельное обновление /status не перезапускает прогон.
	let runId = 0;
	$effect(() => {
		if (!open || !candidate) return;
		result = null;
		failure = '';
		phase = 'running';
		const current = ++runId;
		const target = candidate;
		void (async () => {
			try {
				const updated = await api.recheckAutolistCandidate(target.id);
				if (runId !== current) return;
				result = updated;
				phase = 'done';
				notifications.success(m.autolist_recheck_ok());
			} catch (e) {
				if (runId !== current) return;
				failure = errorMessage(e, m.autolist_recheck_err());
				phase = 'error';
				notifications.error(failure);
			}
		})();
	});

	// Закрытая модалка гасит фазу: следующий открытие стартует прогон заново.
	$effect(() => {
		if (!open) phase = 'idle';
	});

	async function recheckAgain() {
		if (!candidate || phase === 'running') return;
		phase = 'running';
		result = null;
		failure = '';
		const current = ++runId;
		const target = candidate;
		try {
			const updated = await api.recheckAutolistCandidate(target.id);
			if (runId !== current) return;
			result = updated;
			phase = 'done';
			notifications.success(m.autolist_recheck_ok());
		} catch (e) {
			if (runId !== current) return;
			failure = errorMessage(e, m.autolist_recheck_err());
			phase = 'error';
			notifications.error(failure);
		}
	}

	/** Отображаемая карточка: свежий результат проверки или входная. */
	let shown = $derived(result ?? candidate);

	function verdictText(v?: string): string {
		switch (v) {
			case 'not-checked': return m.autolist_verdict_not_checked();
			case 'blocked': return m.autolist_verdict_blocked();
			case 'reachable': return m.autolist_verdict_reachable();
			case 'error': return m.autolist_verdict_error();
			default: return '—';
		}
	}

	/** Статус проверки интернета: «интернет работает» вместо «открывается». */
	function wanVerdictText(v?: string): string {
		switch (v) {
			case 'not-checked': return m.autolist_wan_verdict_not_checked();
			case 'blocked': return m.autolist_wan_verdict_blocked();
			case 'reachable': return m.autolist_wan_verdict_reachable();
			case 'error': return m.autolist_wan_verdict_error();
			default: return '—';
		}
	}

	function verdictClass(v?: string): string {
		switch (v) {
			case 'blocked': return 'text-error';
			case 'reachable': return 'text-success';
			case 'error': return 'text-warning';
			default: return 'opacity-60';
		}
	}

	function reasonText(reason: string): string {
		switch (reason) {
			case 'tcp-rst': return m.autolist_reason_tcp_reset();
			case 'tls-no-response': return m.autolist_reason_tls_no_response();
			case 'connect-no-response': return m.autolist_reason_connect_no_response();
			case 'response-stall': return m.autolist_reason_response_stall();
			case 'udp-no-response': return m.autolist_reason_udp_no_response();
			default: return reason;
		}
	}
</script>

<Modal {open} title={m.autolist_recheck_title()} onclose={onclose}>
	{#if phase === 'running'}
		<div class="flex flex-col items-center gap-3 py-6" data-testid="recheck-running">
			<div class="w-8 h-8 border-2 border-primary-500 border-t-transparent rounded-full animate-spin" aria-hidden="true"></div>
			<p class="text-sm opacity-60">{m.autolist_recheck_running()}</p>
		</div>
	{:else if shown}
		{#if phase === 'error'}
			<div class="text-error text-sm break-all mb-2" data-testid="recheck-error">{failure}</div>
		{/if}
		{#if phase === 'done' && result === null}
			<div class="text-warning text-sm mb-2">{m.autolist_recheck_gone()}</div>
		{/if}
		<!-- Тот же стиль доказательств, что у карточки кандидата в таблице:
		     Reason/WAN · Direct/Tunnel · First seen/Last seen. -->
		<div class="autolist-recheck-evidence text-xs">
			<div class="flex flex-col gap-1">
				<span>
					<span class="opacity-60">{m.autolist_col_reason()}:</span>
					{reasonText(shown.reason)}
				</span>
				<span>
					<span class="opacity-60">{m.autolist_col_wan()}:</span>
					<span class={verdictClass(shown.wan?.verdict)}>{wanVerdictText(shown.wan?.verdict)}</span>
					<span class="opacity-60">{formatAutolistDuration(shown.wan?.durationMs)}</span>
				</span>
			</div>
			<div class="flex flex-col gap-1">
				<span>
					<span class="opacity-60">{m.autolist_col_direct()}:</span>
					<span class={verdictClass(shown.direct?.verdict)}>{verdictText(shown.direct?.verdict)}</span>
					<span class="opacity-60">{formatAutolistDuration(shown.direct?.durationMs)}</span>
				</span>
				<span>
					<span class="opacity-60">{m.autolist_col_tunnel()}:</span>
					<span class={verdictClass(shown.tunnel?.verdict)}>{verdictText(shown.tunnel?.verdict)}</span>
					<span class="opacity-60">{formatAutolistDuration(shown.tunnel?.durationMs)}</span>
				</span>
			</div>
			<div class="flex flex-col gap-1">
				<span class="opacity-60">{m.autolist_col_first_seen()}: {formatAutolistTime(shown.firstAt)}</span>
				<span class="opacity-60">{m.autolist_col_last_seen()}: {formatAutolistTime(shown.lastAt)}</span>
			</div>
		</div>
	{#if shown.lastError}
			<div class="text-error text-xs break-all mt-2">
				<span class="opacity-60">{m.autolist_last_error()}:</span>
				{shown.lastError}
			</div>
	{/if}
		<!-- Детали проверки: тот же блок, что у карточки кандидата. -->
		{@render details?.(shown, false)}
	{/if}
	{#snippet actions()}
		<!-- Перенос строк: набор действий шире карточки и не должен вылезать за её край. -->
		<div class="flex flex-wrap justify-end gap-2">
			{#if phase !== 'running' && candidate}
				<Button variant="secondary" size="md" onclick={() => void recheckAgain()}>
					{#snippet iconBefore()}
						<RotateCw size={14} strokeWidth={2} aria-hidden="true" />
					{/snippet}
					{m.autolist_recheck_again()}
				</Button>
			{/if}
			{@render actions?.()}
		</div>
	{/snippet}
</Modal>

<style>
	/* Тот же grid доказательств, что в AutolistTab: три колонки, на узких
	   экранах — столбец. Локальная копия: вкладку редактирует другой поток. */
	.autolist-recheck-evidence {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.25rem 1rem;
	}
	@media (max-width: 640px) {
		.autolist-recheck-evidence {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
