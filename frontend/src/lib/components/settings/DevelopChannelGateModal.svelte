<script lang="ts">
	import { m } from '$lib/i18n';
	import RichText from '$lib/components/ui/RichText.svelte';
	import { Modal, Button, ConfirmModal } from '$lib/components/ui';
	import { isMockDevMode } from '$lib/env';
	import { openDonateModal } from '$lib/stores/donateModal';
	import type { DevelopQuizQuestion } from '$lib/utils/developChannelGate';
	import {
		DEVELOP_CHANNEL_DOCS_URL,
		clearDevelopChannelLockout,
		pickCopyCheatOption,
		DEVELOP_CHANNEL_QUIZ_MAX_WRONG,
		DEVELOP_CHANNEL_QUIZ_QUESTION_MS,
		DEVELOP_CHANNEL_QUIZ_SIZE,
		formatDevelopChannelLockoutDurationLabel,
		formatLockoutCountdown,
		formatQuizQuestionCountdown,
		getDevelopChannelLockoutRemainingMs,
		isDevelopQuizPassed,
		prepareDevelopQuizSession,
		markDevelopChannelQuizPassed,
		scoreDevelopQuiz,
		resolveDevelopChannelLockoutMs,
		setDevelopChannelLockout,
	} from '$lib/utils/developChannelGate';

	interface Props {
		open: boolean;
		busy?: boolean;
		onclose: () => void;
		onpassed: () => void | Promise<void>;
	}

	let { open, busy = false, onclose, onpassed }: Props = $props();

	type Phase = 'lockout' | 'disclaimer' | 'quiz' | 'result';

	const disclaimerItems = $derived([
		m.settings_develop_gate_item_1,
		m.settings_develop_gate_item_2,
		m.settings_develop_gate_item_3,
		m.settings_develop_gate_item_4,
		m.settings_develop_gate_item_5,
		m.settings_develop_gate_item_6
	]);

	let phase = $state<Phase>('disclaimer');
	let lockoutRemainingMs = $state(0);
	let quizQuestions = $state<DevelopQuizQuestion[]>([]);
	let quizIndex = $state(0);
	let answers = $state<Record<string, number>>({});
	let resultCorrect = $state(0);
	let resultPassed = $state(false);
	let lockoutTick: ReturnType<typeof setInterval> | null = null;
	let questionTimerTick: ReturnType<typeof setInterval> | null = null;
	let questionRemainingMs = $state(DEVELOP_CHANNEL_QUIZ_QUESTION_MS);
	let quitConfirmOpen = $state(false);
	let gateBodyEl: HTMLDivElement | null = $state(null);
	/** question id → случайная «признательная» фраза после попытки копирования */
	let cheatCaughtByQuestionId = $state<Record<string, string>>({});
	let awgmCheatBuffer = $state('');

	const currentQuestion = $derived(quizQuestions[quizIndex] ?? null);
	const selectedIndex = $derived(
		currentQuestion ? answers[currentQuestion.id] : undefined,
	);
	const progressLabel = $derived(
		phase === 'quiz' && quizQuestions.length > 0
			? m.settings_develop_gate_progress({ current: quizIndex + 1, total: quizQuestions.length })
			: '',
	);
	const progressPercent = $derived(
		quizQuestions.length > 0 ? ((quizIndex + 1) / quizQuestions.length) * 100 : 0,
	);
	const copyCheatActive = $derived(
		currentQuestion ? !!cheatCaughtByQuestionId[currentQuestion.id] : false,
	);
	const displayedOptions = $derived(
		copyCheatActive && currentQuestion
			? [cheatCaughtByQuestionId[currentQuestion.id]]
			: (currentQuestion?.options ?? []),
	);

	const lockoutDurationLabel = $derived(formatDevelopChannelLockoutDurationLabel(isMockDevMode()));

	const modalTitle = $derived(
		phase === 'lockout'
			? m.settings_develop_gate_title_lockout()
			: phase === 'disclaimer'
				? m.settings_develop_gate_title_disclaimer()
				: phase === 'quiz'
					? m.settings_develop_gate_title_quiz()
					: resultPassed
						? m.settings_develop_gate_title_passed()
						: m.settings_develop_gate_title_failed(),
	);

	function refreshLockoutRemaining() {
		lockoutRemainingMs = getDevelopChannelLockoutRemainingMs();
	}

	function stopLockoutTimer() {
		if (lockoutTick) {
			clearInterval(lockoutTick);
			lockoutTick = null;
		}
	}

	function startLockoutTimer() {
		stopLockoutTimer();
		refreshLockoutRemaining();
		lockoutTick = setInterval(() => {
			refreshLockoutRemaining();
			if (lockoutRemainingMs <= 0) {
				stopLockoutTimer();
				if (open && phase === 'lockout') {
					phase = 'disclaimer';
				}
			}
		}, 1000);
	}

	function resetQuizState() {
		quizIndex = 0;
		answers = {};
		cheatCaughtByQuestionId = {};
		resultCorrect = 0;
		resultPassed = false;
	}

	function startQuizSession() {
		quizQuestions = prepareDevelopQuizSession(DEVELOP_CHANNEL_QUIZ_SIZE);
		resetQuizState();
	}

	function initPhase() {
		awgmCheatBuffer = '';
		refreshLockoutRemaining();
		if (lockoutRemainingMs > 0) {
			phase = 'lockout';
			startLockoutTimer();
			return;
		}
		stopLockoutTimer();
		phase = 'disclaimer';
		quizQuestions = [];
		resetQuizState();
	}

	$effect(() => {
		if (open) {
			initPhase();
		} else {
			stopLockoutTimer();
			stopQuestionTimer();
			awgmCheatBuffer = '';
			quitConfirmOpen = false;
		}
	});

	$effect(() => {
		if (!open || phase !== 'quiz' || quizQuestions.length === 0) {
			stopQuestionTimer();
			return;
		}
		// Restart per-question countdown when the index changes.
		quizIndex;
		startQuestionTimer();
		return () => stopQuestionTimer();
	});

	function stopQuestionTimer() {
		if (questionTimerTick) {
			clearInterval(questionTimerTick);
			questionTimerTick = null;
		}
	}

	function startQuestionTimer() {
		stopQuestionTimer();
		questionRemainingMs = DEVELOP_CHANNEL_QUIZ_QUESTION_MS;
		questionTimerTick = setInterval(() => {
			questionRemainingMs = Math.max(0, questionRemainingMs - 1000);
			if (questionRemainingMs <= 0) {
				stopQuestionTimer();
				advanceOnQuestionTimeout();
			}
		}, 1000);
	}

	function handleClose() {
		onclose();
	}

	function requestModalClose() {
		if (phase === 'quiz') {
			quitConfirmOpen = true;
			return;
		}
		handleClose();
	}

	function acceptDisclaimer() {
		startQuizSession();
		phase = 'quiz';
	}

	function selectOption(index: number) {
		if (!currentQuestion) return;
		answers = { ...answers, [currentQuestion.id]: index };
	}

	function goNextQuestion() {
		if (selectedIndex === undefined) return;
		if (quizIndex < quizQuestions.length - 1) {
			quizIndex += 1;
			return;
		}
		submitQuiz();
	}

	function advanceOnQuestionTimeout() {
		if (quizIndex < quizQuestions.length - 1) {
			quizIndex += 1;
			return;
		}
		submitQuiz();
	}

	function revealCopyCheatOptions() {
		if (!currentQuestion || copyCheatActive) return;
		cheatCaughtByQuestionId = {
			...cheatCaughtByQuestionId,
			[currentQuestion.id]: pickCopyCheatOption(),
		};
		const next = { ...answers };
		delete next[currentQuestion.id];
		answers = next;
	}

	function applyQuizFailureLockout() {
		setDevelopChannelLockout(resolveDevelopChannelLockoutMs(isMockDevMode()));
		refreshLockoutRemaining();
		startLockoutTimer();
	}

	function completeQuizWithAwgmCheat() {
		const completedQuestions = quizQuestions.length > 0
			? quizQuestions
			: prepareDevelopQuizSession(DEVELOP_CHANNEL_QUIZ_SIZE);
		quizQuestions = completedQuestions;
		quizIndex = Math.max(0, completedQuestions.length - 1);
		answers = {};
		cheatCaughtByQuestionId = {};
		resultCorrect = completedQuestions.length;
		resultPassed = true;
		quitConfirmOpen = false;
		awgmCheatBuffer = '';
		stopQuestionTimer();
		stopLockoutTimer();
		clearDevelopChannelLockout();
		markDevelopChannelQuizPassed();
		phase = 'result';
	}

	function submitQuiz() {
		stopQuestionTimer();
		const correct = scoreDevelopQuiz(quizQuestions, answers, cheatCaughtByQuestionId);
		resultCorrect = correct;
		resultPassed = isDevelopQuizPassed(correct, quizQuestions.length);
		if (resultPassed) {
			markDevelopChannelQuizPassed();
		} else {
			applyQuizFailureLockout();
		}
		phase = 'result';
	}

	function failQuizAbandoned() {
		stopQuestionTimer();
		quitConfirmOpen = false;
		resultCorrect = scoreDevelopQuiz(quizQuestions, answers, cheatCaughtByQuestionId);
		resultPassed = false;
		applyQuizFailureLockout();
		phase = 'result';
	}

	async function confirmPassed() {
		await onpassed();
	}

	function handleAwgmCheatKeydown(e: KeyboardEvent): boolean {
		if (!open || quitConfirmOpen) return false;
		if (e.ctrlKey || e.metaKey || e.altKey) return false;
		if (e.key.length !== 1) return false;
		const key = e.key.toLowerCase();
		if (!/^[a-z]$/.test(key)) return false;
		awgmCheatBuffer = `${awgmCheatBuffer}${key}`.slice(-4);
		if (awgmCheatBuffer !== 'awgm') return false;
		e.preventDefault();
		completeQuizWithAwgmCheat();
		return true;
	}

	function handleQuizKeydown(e: KeyboardEvent) {
		if (handleAwgmCheatKeydown(e)) return;
		if (!open || phase !== 'quiz' || quitConfirmOpen) return;

		const target = e.target as HTMLElement | null;
		if (target?.closest('.modal-footer')) return;

		if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === 'c') {
			e.preventDefault();
			revealCopyCheatOptions();
			return;
		}

		if (e.key !== 'Enter') return;

		if (selectedIndex !== undefined) {
			e.preventDefault();
			goNextQuestion();
			return;
		}

		// First Enter on a focused option: native button activation selects it.
		if (target?.closest('.option')) return;
	}

	function handleQuizCopy(e: ClipboardEvent) {
		if (!open || phase !== 'quiz' || quitConfirmOpen) return;
		const target = e.target;
		if (!target || !gateBodyEl?.contains(target as Node)) return;
		e.preventDefault();
		revealCopyCheatOptions();
	}

</script>

<svelte:window onkeydown={handleQuizKeydown} />
<svelte:document oncopycapture={handleQuizCopy} />

<Modal
	{open}
	title={modalTitle}
	size={phase === 'quiz' ? 'lg' : 'md'}
	onclose={requestModalClose}
	closeOnBackdrop={phase !== 'quiz'}
>
	<div class="gate-body" bind:this={gateBodyEl}>
		{#if phase === 'lockout'}
			<p class="gate-lead">
				{m.settings_develop_gate_lockout_lead()}
			</p>
			<div class="lockout-timer" aria-live="polite">
				<span class="lockout-timer-label">{m.settings_develop_gate_retry_in()}</span>
				<span class="lockout-timer-value">{formatLockoutCountdown(lockoutRemainingMs)}</span>
			</div>
			<p class="gate-hint">
				{m.settings_develop_gate_lockout_hint({ duration: lockoutDurationLabel })}
			</p>
			<p class="gate-docs-hint">
				{m.settings_develop_gate_docs_hint()}
				<a href={DEVELOP_CHANNEL_DOCS_URL} target="_blank" rel="noopener noreferrer">{m.settings_develop_gate_docs_link()}</a>.
			</p>
			<p class="gate-premium-hint">
				{m.settings_develop_gate_premium_hint()}
				<button type="button" class="gate-premium-highlight" onclick={openDonateModal}>
					{m.settings_develop_gate_premium_link()}
				</button> {m.settings_develop_gate_premium_suffix()}
			</p>
		{:else if phase === 'disclaimer'}
			<p class="gate-lead">
				<RichText text={m.settings_develop_gate_disclaimer_lead()} />
			</p>
			<ul class="disclaimer-list">
				{#each disclaimerItems as item}
					<li>{item}</li>
				{/each}
			</ul>
		{:else if phase === 'quiz' && currentQuestion}
			<div class="quiz-progress" aria-label={progressLabel}>
				<div class="quiz-progress-row">
					<div class="quiz-progress-leading">
						<span class="quiz-progress-text">{progressLabel}</span>
						<span class="quiz-progress-score">
							{m.settings_develop_gate_allowed_errors({ count: DEVELOP_CHANNEL_QUIZ_MAX_WRONG })}
						</span>
					</div>
					<div
						class="quiz-timer"
						class:quiz-timer-urgent={questionRemainingMs <= 5_000}
						aria-live="polite"
						title={m.settings_develop_gate_time_to_answer()}
					>
						{formatQuizQuestionCountdown(questionRemainingMs)}
					</div>
				</div>
				<div class="quiz-progress-track">
					<div class="quiz-progress-fill" style:width="{progressPercent}%"></div>
				</div>
			</div>

			<p class="quiz-question" class:quiz-question-copy-caught={copyCheatActive}>
				{currentQuestion.text}
			</p>

			<div
				class="options"
				class:options-copy-caught={copyCheatActive}
				role="radiogroup"
				aria-label={currentQuestion.text}
			>
				{#each displayedOptions as option, index}
					<button
						type="button"
						class="option"
						class:option-copy-cheat={copyCheatActive}
						class:selected={selectedIndex === index}
						aria-pressed={selectedIndex === index}
						onclick={() => selectOption(index)}
					>
						<span class="option-marker" aria-hidden="true"></span>
						<span class="option-text">{option}</span>
					</button>
				{/each}
			</div>
		{:else if phase === 'result'}
			<div class="result-banner" class:passed={resultPassed} class:failed={!resultPassed}>
				<span class="result-score">{resultCorrect} / {quizQuestions.length}</span>
				<span class="result-caption">
					{#if resultPassed}
						{m.settings_develop_gate_passed_text()}
					{:else}
						{m.settings_develop_gate_result_allowed({ count: DEVELOP_CHANNEL_QUIZ_MAX_WRONG, total: quizQuestions.length, duration: lockoutDurationLabel })}
					{/if}
				</span>
			</div>
			{#if !resultPassed}
				<div class="lockout-timer lockout-timer-inline" aria-live="polite">
					<span class="lockout-timer-label">{m.settings_develop_gate_next_attempt()}</span>
					<span class="lockout-timer-value">{formatLockoutCountdown(lockoutRemainingMs)}</span>
				</div>
				<p class="gate-docs-hint">
					{m.settings_develop_gate_docs_hint()}
					<a href={DEVELOP_CHANNEL_DOCS_URL} target="_blank" rel="noopener noreferrer">{m.settings_develop_gate_docs_link()}</a>.
				</p>
				<p class="gate-premium-hint">
					{m.settings_develop_gate_premium_hint()}
					<button type="button" class="gate-premium-highlight" onclick={openDonateModal}>
						{m.settings_develop_gate_premium_link()}
					</button> {m.settings_develop_gate_premium_suffix()}
				</p>
			{/if}
		{/if}
	</div>

	{#snippet actions()}
		{#if phase === 'lockout'}
			<Button variant="primary" size="md" onclick={handleClose}>{m.settings_develop_gate_understood()}</Button>
		{:else if phase === 'disclaimer'}
			<Button variant="secondary" size="md" onclick={handleClose} disabled={busy}>
				{m.common_cancel()}
			</Button>
			<Button variant="outline-primary" size="md" onclick={acceptDisclaimer} disabled={busy}>
				{m.settings_develop_gate_agree()}
			</Button>
		{:else if phase === 'quiz'}
			<Button variant="secondary" size="md" onclick={requestModalClose} disabled={busy}>
				{m.common_cancel()}
			</Button>
			<Button
				variant="primary"
				size="md"
				onclick={goNextQuestion}
				disabled={busy || selectedIndex === undefined}
			>
				{quizIndex < quizQuestions.length - 1 ? m.settings_develop_gate_next() : m.settings_develop_gate_check_answers()}
			</Button>
		{:else if phase === 'result'}
			{#if resultPassed}
				<Button variant="secondary" size="md" onclick={handleClose} disabled={busy}>
					{m.settings_develop_gate_changed_mind()}
				</Button>
				<Button variant="primary" size="md" onclick={confirmPassed} disabled={busy} loading={busy}>
					{m.settings_develop_gate_go_develop()}
				</Button>
			{:else}
				<Button variant="primary" size="md" onclick={handleClose}>{m.common_close()}</Button>
			{/if}
		{/if}
	{/snippet}
</Modal>

<ConfirmModal
	open={quitConfirmOpen}
	title={m.settings_develop_gate_quit_title()}
	message={m.settings_develop_gate_quit_message()}
	secondary={m.settings_develop_gate_quit_secondary({ duration: lockoutDurationLabel })}
	confirmLabel={m.settings_develop_gate_quit_confirm()}
	cancelLabel={m.settings_develop_gate_quit_cancel()}
	variant="danger"
	onConfirm={failQuizAbandoned}
	onClose={() => (quitConfirmOpen = false)}
/>

<style>
	.gate-body {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.quiz-timer {
		font-variant-numeric: tabular-nums;
		font-size: 0.875rem;
		font-weight: 600;
		padding: 0.2rem 0.5rem;
		border-radius: 0.5rem;
		border: 1px solid var(--border);
		background: var(--surface-elevated, var(--bg-secondary));
		color: var(--text-secondary);
	}

	.quiz-timer-urgent {
		border-color: color-mix(in srgb, var(--danger, #ef4444) 45%, var(--border));
		color: var(--danger, #ef4444);
		background: color-mix(in srgb, var(--danger, #ef4444) 10%, transparent);
	}

	.gate-lead {
		margin: 0;
		line-height: 1.5;
		color: var(--text-primary);
	}

	.gate-hint {
		margin: 0;
		font-size: 0.875rem;
		line-height: 1.45;
		color: var(--text-muted);
	}

	.gate-docs-hint {
		margin: 0;
		font-size: 0.875rem;
		line-height: 1.45;
		color: var(--text-muted);
		text-align: center;
	}

	.gate-docs-hint a {
		color: var(--accent);
		text-decoration: underline;
		text-underline-offset: 2px;
	}

	.gate-docs-hint a:hover {
		color: color-mix(in srgb, var(--accent) 80%, var(--text-primary));
	}

	.gate-premium-hint {
		margin: 0;
		font-size: 0.875rem;
		line-height: 1.45;
		color: var(--text-muted);
		text-align: center;
	}

	.gate-premium-highlight {
		display: inline;
		padding: 0;
		margin: 0;
		border: none;
		background: none;
		font: inherit;
		font-weight: 600;
		color: var(--text-primary);
		cursor: pointer;
		text-decoration: underline;
		text-decoration-color: var(--warning, #e0af68);
		text-underline-offset: 3px;
		text-decoration-thickness: 2px;
	}

	.gate-premium-highlight:hover {
		color: var(--warning, #e0af68);
	}

	.disclaimer-list {
		margin: 0;
		padding: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
		line-height: 1.45;
		color: var(--text-secondary);
	}

	.disclaimer-list li {
		position: relative;
		padding-left: 1.1rem;
	}

	.disclaimer-list li::before {
		content: '';
		position: absolute;
		left: 0;
		top: 0.55em;
		width: 0.35rem;
		height: 0.35rem;
		border-radius: 50%;
		background: var(--accent);
		transform: translateY(-50%);
	}

	.lockout-timer {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.35rem;
		padding: 1.25rem 1rem;
		border-radius: 0.75rem;
		border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border));
		background: color-mix(in srgb, var(--accent) 8%, var(--surface-elevated, var(--bg-secondary)));
	}

	.lockout-timer-inline {
		margin-top: 0.25rem;
	}

	.lockout-timer-label {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.lockout-timer-value {
		font-variant-numeric: tabular-nums;
		font-size: 2rem;
		font-weight: 700;
		letter-spacing: 0.02em;
		color: var(--accent);
	}

	.quiz-progress {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.quiz-progress-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
	}

	.quiz-progress-row .quiz-timer {
		flex-shrink: 0;
	}

	.quiz-progress-leading {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		min-width: 0;
	}

	.quiz-progress-text {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.quiz-progress-score {
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.quiz-progress-track {
		height: 6px;
		border-radius: 999px;
		background: var(--border);
		overflow: hidden;
	}

	.quiz-progress-fill {
		height: 100%;
		border-radius: inherit;
		background: linear-gradient(90deg, var(--accent), color-mix(in srgb, var(--accent) 65%, #fff));
		transition: width 0.25s ease;
	}

	.quiz-question {
		margin: 0.25rem 0 0;
		font-size: 1.05rem;
		font-weight: 600;
		line-height: 1.4;
		user-select: none;
	}

	.quiz-question-copy-caught {
		opacity: 0.55;
	}

	.options-copy-caught {
		margin-top: 0.25rem;
	}

	.option-copy-cheat {
		border-color: color-mix(in srgb, var(--warning, #e0af68) 45%, var(--border));
		background: color-mix(in srgb, var(--warning, #e0af68) 8%, transparent);
	}

	.option-copy-cheat.selected {
		border-color: var(--warning, #e0af68);
		background: color-mix(in srgb, var(--warning, #e0af68) 14%, transparent);
	}

	.options {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.option {
		display: flex;
		align-items: flex-start;
		gap: 0.75rem;
		width: 100%;
		padding: 0.75rem 0.85rem;
		text-align: left;
		border: 1px solid var(--border);
		border-radius: 0.65rem;
		background: var(--surface, transparent);
		color: var(--text-primary);
		cursor: pointer;
		transition:
			border-color 0.15s ease,
			background 0.15s ease,
			box-shadow 0.15s ease;
	}

	.option:hover {
		border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
		background: color-mix(in srgb, var(--accent) 6%, transparent);
	}

	.option.selected {
		border-color: var(--accent);
		background: color-mix(in srgb, var(--accent) 12%, transparent);
		box-shadow: 0 0 0 1px color-mix(in srgb, var(--accent) 25%, transparent);
	}

	.option-marker {
		flex-shrink: 0;
		width: 1rem;
		height: 1rem;
		margin-top: 0.15rem;
		border-radius: 50%;
		border: 2px solid var(--border);
		background: transparent;
		transition:
			border-color 0.15s ease,
			background 0.15s ease;
	}

	.option.selected .option-marker {
		border-color: var(--accent);
		background: radial-gradient(circle at center, var(--accent) 42%, transparent 44%);
	}

	.option-text {
		line-height: 1.4;
		font-size: 0.9rem;
	}

	.result-banner {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
		padding: 1.25rem 1rem;
		border-radius: 0.75rem;
		text-align: center;
	}

	.result-banner.passed {
		border: 1px solid color-mix(in srgb, var(--success, #22c55e) 40%, var(--border));
		background: color-mix(in srgb, var(--success, #22c55e) 10%, transparent);
	}

	.result-banner.failed {
		border: 1px solid color-mix(in srgb, var(--danger, #ef4444) 35%, var(--border));
		background: color-mix(in srgb, var(--danger, #ef4444) 8%, transparent);
	}

	.result-score {
		font-size: 2rem;
		font-weight: 700;
		font-variant-numeric: tabular-nums;
	}

	.result-banner.passed .result-score {
		color: var(--success, #22c55e);
	}

	.result-banner.failed .result-score {
		color: var(--danger, #ef4444);
	}

	.result-caption {
		margin: 0;
		line-height: 1.45;
		color: var(--text-secondary);
		max-width: 28rem;
	}
</style>
