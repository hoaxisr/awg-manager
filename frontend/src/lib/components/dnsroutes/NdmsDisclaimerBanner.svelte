<!--
  Ограничения NDMS-движка DNS-маршрутизации + подтверждение понимания. Три состояния:
  список ограничений → квиз из трёх вопросов → компактная строка «принято».
  Подтверждение живёт в localStorage (per-browser), backend о нём не знает.
-->
<script lang="ts">
	import { CircleCheck, TriangleAlert } from 'lucide-svelte';
	import { Button } from '$lib/components/ui';
	import { createPersistedFlag } from '$lib/stores/persisted';

	interface Props {
		/** NDMS-движок DNS-маршрутизации существует только на OS5. */
		isOS5?: boolean;
	}

	let { isOS5 = false }: Props = $props();

	const acked = createPersistedFlag('awgm.ndmsDisclaimerAck', false);

	const LIMITS = [
		'Маршрут работает только для клиентов в политике «Политика по умолчанию». Устройство, привязанное к своей политике доступа, под DNS-маршруты не попадает.',
		'Своё шифрованное DNS (DoH/DoT) на устройстве или в браузере обходит маршрут — запрос не проходит через резолвер роутера.',
		'Первые запросы к домену могут уйти мимо маршрута: адреса попадают в таблицу маршрутизации уже после ответа DNS.',
		'Совпадение идёт по суффиксу: правило на x.com поймает и yandex.com.',
		'Домены с коротким TTL могут не попадать в таблицу маршрутизации.',
		'Стороннее ПО в Entware, перехватывающее DNS-запросы, ломает механизм.',
	];

	interface Question {
		question: string;
		options: string[];
		correct: number;
		explain: string;
	}

	const QUIZ: Question[] = [
		{
			question:
				'Устройство привязано к своей политике доступа, а не к «Политике по умолчанию». Попадёт ли его трафик в DNS-маршрут?',
			options: ['Да, политика на DNS-маршруты не влияет', 'Нет'],
			correct: 1,
			explain: 'DNS-маршрутизация обслуживает только «Политику по умолчанию».',
		},
		{
			question: 'В браузере на устройстве включён свой DoH. Сработает ли DNS-маршрут?',
			options: ['Да, роутер всё равно увидит запрос', 'Нет: запрос уходит мимо резолвера роутера'],
			correct: 1,
			explain: 'Роутер не видит шифрованный запрос и не может добавить адрес в маршрут.',
		},
		{
			question: 'Правило создано на домен x.com. Что ещё попадёт под правило?',
			options: ['Только x.com и его поддомены', 'Любой домен с таким суффиксом, например yandex.com'],
			correct: 1,
			explain: 'Совпадение идёт по суффиксу строки, а не по границе домена.',
		},
	];

	let quizOpen = $state(false);
	let answers = $state<(number | null)[]>(QUIZ.map(() => null));
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
			<span>Ограничения NDMS-маршрутизации приняты</span>
			<button class="ack-link" onclick={() => (reopened = true)}>Показать</button>
		</div>
	{:else}
		<div class="ndms-disclaimer" role="note">
			<div class="head">
				<TriangleAlert size={20} />
				<h4>Ограничения DNS-маршрутизации NDMS</h4>
			</div>

			{#if quizOpen && !$acked}
				<ol class="quiz">
					{#each QUIZ as q, i}
						<li>
							<p class="question">{q.question}</p>
							<div class="options">
								{#each q.options as opt, k}
									<button
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
					Подробности на форуме Keenetic:
					<a
						href="https://forum.keenetic.ru/topic/23098-web-маршрутизация-маршруты-dns/"
						target="_blank"
						rel="noopener">маршруты DNS</a
					>,
					<a
						href="https://forum.keenetic.ru/topic/25147-вопросы-связанные-с-работой-dns-маршрутизации-dns-routing/"
						target="_blank"
						rel="noopener">вопросы по DNS routing</a
					>.
				</p>
				{#if $acked}
					<Button variant="ghost" size="sm" onclick={() => (reopened = false)}>Свернуть</Button>
				{:else}
					<Button variant="secondary" size="sm" onclick={() => (quizOpen = true)}>
						Я прочитал — проверить себя
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
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--color-text-secondary, var(--text-secondary));
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
		background: var(--surface-2, transparent);
		border: 1px solid var(--border, rgba(128, 128, 128, 0.35));
		border-radius: var(--radius-sm, 6px);
		cursor: pointer;
	}

	.option.correct {
		border-color: var(--color-success, var(--success));
		color: var(--color-success, var(--success));
	}

	.option.wrong {
		border-color: var(--color-danger, var(--danger));
		color: var(--color-danger, var(--danger));
	}

	.explain {
		margin: 0.5rem 0 0;
		font-size: 0.8125rem;
		color: var(--color-danger, var(--danger));
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
		color: var(--color-primary, var(--primary));
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
