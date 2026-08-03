<script lang="ts">
	import { goto } from '$app/navigation';
	import { Button, Modal } from '$lib/components/ui';
	import { isMockDevMode } from '$lib/env';
	import { developFeedbackFabVisible } from '$lib/stores/developFeedbackFab';
	import { requestDevelopFeedbackIncident } from '$lib/stores/developFeedbackIncident';
	import { buildSuggestionIssueUrl } from '$lib/utils/githubFeedback';

	let modalOpen = $state(false);
	const showFab = $derived($developFeedbackFabVisible && !isMockDevMode());

	const suggestionIssueUrl = buildSuggestionIssueUrl();

	function openModal() {
		modalOpen = true;
	}

	function closeModal() {
		modalOpen = false;
	}

	function handleIncident() {
		closeModal();
		requestDevelopFeedbackIncident();
		void goto('/diagnostics?tab=checks');
	}

	function goToFeedbackSetting(e: MouseEvent) {
		e.preventDefault();
		closeModal();
		void goto('/settings?feedbackFab');
	}
</script>

{#if showFab}
	<button
		type="button"
		class="fab"
		aria-label="Обратная связь"
		title="Сообщить об ошибке или предложить улучшение"
		onclick={openModal}
	>
		!
	</button>
{/if}

<Modal open={modalOpen} title="Обратная связь" size="md" onclose={closeModal}>
	<div class="body">
		<p>
			Вы можете создать тикет на GitHub: сообщить об ошибке, предложить улучшение
			или задать вопрос по develop-сборке. Ответ не гарантируется — это публичный
			open-source проект без службы поддержки.
		</p>
		<p>
			Если эта кнопка мешает, её можно скрыть в
			<a href="/settings?feedbackFab" onclick={goToFeedbackSetting}>настройках</a>.
		</p>
	</div>

	{#snippet actions()}
		<div class="feedback-actions">
			<Button
				variant="secondary"
				size="md"
				fullWidth
				href={suggestionIssueUrl}
				target="_blank"
				rel="noopener noreferrer"
				onclick={closeModal}
			>
				<span class="split-label">
					<span>Сообщение /</span>
					<span class="split-second">предложение</span>
				</span>
			</Button>
			<Button variant="outline-danger" size="md" fullWidth onclick={handleIncident}>
				<span class="split-label">
					<span>Инцидент /</span>
					<span class="split-second">ошибка</span>
				</span>
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.fab {
		position: fixed;
		right: 1.25rem;
		bottom: 1.25rem;
		z-index: var(--z-fab);
		width: 3rem;
		height: 3rem;
		border: 2px solid var(--color-error-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		color: var(--color-error);
		font-size: 1.375rem;
		font-weight: 700;
		line-height: 1;
		cursor: pointer;
		box-shadow: 0 4px 16px rgba(0, 0, 0, 0.35);
		transition:
			background var(--t-fast) ease,
			transform var(--t-fast) ease,
			filter var(--t-fast) ease;
	}

	.fab:hover {
		background: var(--color-error-tint);
		transform: scale(1.05);
	}

	.fab:active {
		transform: scale(0.98);
	}

	.body {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		font-size: 0.875rem;
		line-height: 1.5;
		color: var(--color-text-secondary);
	}

	.body p {
		margin: 0;
	}

	.body a {
		color: var(--color-accent);
		text-decoration: none;
	}

	.body a:hover {
		text-decoration: underline;
	}

	.feedback-actions {
		display: flex;
		flex: 1 1 100%;
		width: 100%;
		min-width: 0;
		gap: 0.5rem;
		justify-content: flex-end;
		container-type: inline-size;
	}

	.feedback-actions :global(.btn) {
		flex: 1 1 0;
		min-width: 0;
		max-width: 100%;
		height: auto;
		min-height: 2rem;
		max-height: none;
		padding-block: 0.375rem;
		white-space: normal;
	}

	.feedback-actions :global(.btn .label) {
		display: block;
		line-height: 1.25;
		text-align: center;
		white-space: normal;
	}

	.split-label {
		display: inline-block;
		text-align: center;
	}

	.split-second {
		display: inline;
	}

	/* Узкие кнопки в футере (мобилка и md-модалка): вторая часть под «/» */
	@container (max-width: 22rem) {
		.split-second {
			display: block;
		}
	}
</style>
