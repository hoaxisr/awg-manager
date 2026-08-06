<script lang="ts">
	// Заглушка страниц группы «Движок» на подэтапе 5D1. Каркас маршрутов уже
	// есть, наполнение — волны 5D2. Заглушка намеренно НЕ имитирует будущий
	// интерфейс: иначе визуальный прогон 5D2 не с чем сравнивать, и «наполнено»
	// станет неотличимо от «нарисовано заранее».
	import { Construction } from 'lucide-svelte';
	import { PageContainer, PageHeader, EmptyState } from '$lib/components/layout';

	let {
		title,
		wave,
		source,
	}: {
		/** Подпись пункта сайдбара — она же заголовок страницы. */
		title: string;
		/** Волна 5D2, которая наполнит страницу: '5D2b', '5D2c', '5D2d'. */
		wave: string;
		/** Откуда приедет содержимое — чтобы волна не искала источник заново. */
		source: string;
	} = $props();
</script>

<svelte:head>
	<title>{title} - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader {title} />
	<div class="stub">
		<EmptyState
			title="Раздел ещё не переехал"
			description="Наполнение — волна {wave}. Содержимое приедет из: {source}."
		>
			{#snippet icon()}
				<Construction />
			{/snippet}
			{#snippet action()}
				<!-- Здесь была ссылка «до волны 5D2x это живёт на „Движке“». Волна
				     5D2a снесла с «Движка» содержимое старой /sb/routing, и ссылка
				     стала бы тупиком: редактора нет ни там, ни где-либо ещё.
				     Строка осталась, чтобы пользователь не искал функцию по
				     разделам. -->
				<p class="where">До волны {wave} этой функции нет в интерфейсе.</p>
			{/snippet}
		</EmptyState>
	</div>
</PageContainer>

<style>
	/* Контейнер-scoped :global — scoped-CSS не достаёт до svg внутри
	   дочернего компонента иконки. */
	.stub :global(svg) {
		color: var(--text-muted);
	}

	.where {
		color: var(--color-text-secondary);
		font-size: 0.875rem;
		/* Узкий экран: строка обязана переноситься по словам, а не уезжать
		   за край — ширину держим по описанию EmptyState. */
		max-width: 24rem;
	}
</style>
