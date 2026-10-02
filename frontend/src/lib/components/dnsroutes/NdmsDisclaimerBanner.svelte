<!--
  Ограничения NDMS-движка DNS-маршрутизации + подтверждение понимания. Три состояния:
  список ограничений → квиз из трёх вопросов → компактная строка «принято».
  Подтверждение живёт в localStorage (per-browser), backend о нём не знает.
-->
<script lang="ts">
	import { CircleCheck, TriangleAlert } from 'lucide-svelte';
	import { Button } from '$lib/components/ui';
	import { createPersistedFlag } from '$lib/stores/persisted';
	import { m } from '$lib/i18n';

	interface Props {
		/** NDMS-движок DNS-маршрутизации существует только на OS5. */
		isOS5?: boolean;
	}

	let { isOS5 = false }: Props = $props();

	const acked = createPersistedFlag('awgm.ndmsDisclaimerAck', false);

	const LIMITS = $derived([
		m.dns_routes_ndms_limit_policy(),
		m.dns_routes_ndms_limit_doh(),
		m.dns_routes_ndms_limit_first_requests(),
		m.dns_routes_ndms_limit_suffix(),
		m.dns_routes_ndms_limit_ttl(),
		m.dns_routes_ndms_limit_entware(),
	]);

	interface Question {
		question: string;
		options: string[];
		correct: number;
		explain: string;
	}

	const QUIZ: Question[] = $derived([
		{
			question: m.dns_routes_ndms_quiz1_question(),
			options: [m.dns_routes_ndms_quiz1_option_yes(), m.dns_routes_ndms_quiz_option_no()],
			correct: 1,
			explain: m.dns_routes_ndms_quiz1_explain(),
		},
		{
			question: m.dns_routes_ndms_quiz2_question(),
			options: [m.dns_routes_ndms_quiz2_option_yes(), m.dns_routes_ndms_quiz2_option_no()],
			correct: 1,
			explain: m.dns_routes_ndms_quiz2_explain(),
		},
		{
			question: m.dns_routes_ndms_quiz3_question(),
			options: [m.dns_routes_ndms_quiz3_option_exact(), m.dns_routes_ndms_quiz3_option_suffix()],
			correct: 1,
			explain: m.dns_routes_ndms_quiz3_explain(),
		},
	]);

	let quizOpen = $state(false);
	let answers = $state<(number | null)[]>([null, null, null]);
	/** В подтверждённом состоянии — показать список ограничений снова. */
	let reopened = $state(false);

	function answer(i: number, option: number): void {
		answers[i] = option;
		if (QUIZ.every((q, k) => answers[k] === q.correct)) acked.set(true);
	}
</script>

{#if isOS5}
	{#if $acked && !reopened}
		<div class="ndms-ack" role="note">
			<CircleCheck size={16} />
			<span>{m.dns_routes_ndms_accepted()}</span>
			<button type="button" class="ack-link" onclick={() => (reopened = true)}>{m.dns_routes_ndms_show()}</button>
		</div>
	{:else}
		<div class="ndms-disclaimer" role="note">
			<div class="head">
				<TriangleAlert size={20} />
				<h4>{m.dns_routes_ndms_title()}</h4>
			</div>

			{#if quizOpen && !$acked}
				<ol class="quiz">
					{#each QUIZ as q, i}
						<li>
							<p class="question">{q.question}</p>
							<div class="options">
								{#each q.options as opt, k}
									<button
										type="button"
										class="option"
										class:correct={answers[i] === k && k === q.correct}
										class:wrong={answers[i] === k && k !== q.correct}
										onclick={() => answer(i, k)}
									>
										{opt}
									</button>
								{/each}
							</div>
							{#if answers[i] !== null && answers[i] !== q.correct}
								<p class="explain">{q.explain}</p>
							{/if}
						</li>
					{/each}
				</ol>
			{:else}
				<ul class="limits">
					{#each LIMITS as limit}
						<li>{limit}</li>
					{/each}
				</ul>
				<p class="sources">
					{m.dns_routes_ndms_sources()}
					<a
						href="https://forum.keenetic.ru/topic/23098-web-маршрутизация-маршруты-dns/"
						target="_blank"
						rel="noopener">{m.dns_routes_ndms_source_dns_routes()}</a
					>,
					<a
						href="https://forum.keenetic.ru/topic/25147-вопросы-связанные-с-работой-dns-маршрутизации-dns-routing/"
						target="_blank"
						rel="noopener">{m.dns_routes_ndms_source_dns_routing()}</a
					>.
				</p>
				{#if $acked}
					<Button variant="ghost" size="sm" onclick={() => (reopened = false)}>{m.dns_routes_ndms_collapse()}</Button>
				{:else}
					<Button variant="secondary" size="sm" onclick={() => (quizOpen = true)}>
						{m.dns_routes_ndms_check_yourself()}
					</Button>
				{/if}
			{/if}
		</div>
	{/if}
{/if}

<style>
	.ndms-disclaimer {
		margin-bottom: 1rem;
		padding: 0.875rem 1rem;
		background: var(--color-warning-tint, rgba(224, 175, 104, 0.1));
		border: 1px solid var(--color-warning-border, rgba(224, 175, 104, 0.4));
		border-left: 3px solid var(--color-warning, var(--warning));
		border-radius: var(--radius-sm, 6px);
	}

	.head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.625rem;
		color: var(--color-warning, var(--warning));
	}

	.head h4 {
		margin: 0;
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary, var(--text-primary));
	}

	.limits,
	.quiz {
		margin: 0 0 0.75rem;
		padding-left: 1.25rem;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.limits {
		list-style: disc;
	}

	.quiz {
		list-style: decimal;
	}

	.limits li,
	.quiz li {
		margin-bottom: 0.375rem;
	}

	.question {
		margin: 0 0 0.5rem;
		color: var(--color-text-primary, var(--text-primary));
	}

	.options {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.option {
		padding: 0.375rem 0.75rem;
		font-size: 0.8125rem;
		text-align: left;
		color: var(--color-text-primary, var(--text-primary));
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px solid var(--color-border, var(--border));
		border-radius: var(--radius-sm, 6px);
		cursor: pointer;
	}

	.option.correct {
		border-color: var(--color-success, var(--success));
		color: var(--color-success, var(--success));
	}

	.option.wrong {
		border-color: var(--color-error, var(--error));
		color: var(--color-error, var(--error));
	}

	.explain {
		margin: 0.5rem 0 0;
		font-size: 0.8125rem;
		color: var(--color-error, var(--error));
	}

	.sources {
		margin: 0 0 0.75rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.ndms-ack {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 1rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.ack-link {
		padding: 0;
		font-size: inherit;
		color: var(--color-accent, var(--accent));
		background: none;
		border: none;
		cursor: pointer;
		text-decoration: underline;
	}

	@media (max-width: 480px) {
		.options {
			flex-direction: column;
		}
	}
</style>
