<script lang="ts">
	// Слой страниц движка sing-box: общие механики, которые обязаны пережить
	// разбор /sb/routing на девять маршрутов.
	//
	// Что здесь и почему именно здесь:
	//
	//  * ModeSwitchHost — подтверждение и прогресс смены режима маршрутизации.
	//    Переключение идёт минуты и продолжается в фоне; если хост живёт на
	//    странице, уход с неё (сайдбар, крошки, back) уносит модалку вместе с
	//    прогрессом. В layout он переживает навигацию по всей группе.
	//
	//  * SelectiveRebuildModal — прогресс пересборки ipset. Пересборку запускает
	//    «Применить» в StagingBanner, а баннер теперь тоже общий: применить
	//    черновик можно с любой страницы группы, значит и окно прогресса обязано
	//    открываться с любой. Состояние открытия держим здесь же — на странице
	//    оно умирало бы вместе с ней.
	//
	//  * StagingBanner — черновик один на весь слот маршрутизации: правка правил,
	//    наборов и outbound'ов с любой страницы копится в общем pending. Баннер
	//    на уровне группы — единственный способ не потерять черновик при переходе
	//    между страницами.
	//
	//  * Гейт «Sing-box не установлен» — по разделу 7 спеки он нужен на ВСЕХ
	//    страницах группы, включая журнал и соединения движка: без пакета там
	//    пустой терминал и пустая таблица, неотличимые от «ничего не происходит».
	//
	//  * Напоминание о непринятом черновике. Раньше оно жило на /sb/routing и
	//    считало границей pathname одной страницы; теперь граница — членство
	//    маршрута в группе (`remindAboutDraft`): ходьба по страницам движка
	//    молчит, потому что баннер черновика идёт вместе с пользователем.
	//
	// Четырёх нетрогаемых страниц (/sb/tunnels, /sb/awg3, /sb/subscriptions,
	// /sb/geodata) этот слой НЕ накрывает — они лежат вне группы (engine).
	import { onMount } from 'svelte';
	import { get } from 'svelte/store';
	import type { Snippet } from 'svelte';
	import { beforeNavigate, goto } from '$app/navigation';
	import { PageContainer, EmptyState } from '$lib/components/layout';
	import { Button, Modal } from '$lib/components/ui';
	import { systemInfo } from '$lib/stores/system';
	import { remindAboutDraft } from '$lib/data/engineDraftGuard';
	import { StagingBanner } from '$lib/components/singbox-routing';
	import ModeSwitchHost from '$lib/components/routing/ModeSwitchHost.svelte';
	import SelectiveRebuildModal from '$lib/components/sb-router/SelectiveRebuildModal.svelte';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { selectiveBypass } from '$lib/stores/selectiveBypass';

	let { children }: { children: Snippet } = $props();

	onMount(() => {
		// Прайминг ровно того, что показывает сам слой: статус черновика. Его
		// грузит только loadAll страницы движка, поэтому на входе прямой ссылкой
		// в любую другую страницу группы баннер молчал бы при живом черновике.
		// Дальше состояние держит SSE (resource singbox.router.staging).
		if (get(singboxRouter.staging) === null) {
			void singboxRouter.loadStaging();
		}
	});

	const { progress: selectiveProgress, modalRequested: selectiveModalRequested } = selectiveBypass;

	let singboxInstalled = $derived($systemInfo.data?.singbox?.installed ?? false);
	// Пока про систему ничего не известно, гейт не показываем: иначе на холодной
	// загрузке страница на секунду говорит «не установлен» про установленный пакет.
	let systemKnown = $derived($systemInfo.lastFetchedAt > 0 || $systemInfo.status === 'error');
	let singboxMissing = $derived(systemKnown && !singboxInstalled);

	// Окно открывается только по явному запросу (кнопка «Применить» или включение
	// движка), а не по любому приходу прогресса по SSE.
	let rebuildOpen = $state(false);
	$effect(() => {
		if ($selectiveModalRequested) {
			rebuildOpen = true;
		}
	});

	function minimizeRebuild(): void {
		rebuildOpen = false;
		selectiveBypass.clearModalRequest();
	}

	function dismissRebuild(): void {
		rebuildOpen = false;
		selectiveBypass.clearModalRequest();
		selectiveBypass.resetProgress();
	}

	// ── Напоминание о непринятом черновике ────────────────────────────────────
	// Куда пользователь собрался, пока напоминание на экране. null — напоминания
	// нет.
	let leavingTo = $state<string | null>(null);
	// Повторный goto после подтверждения не должен снова упереться в гард.
	let leaving = $state(false);

	beforeNavigate((nav) => {
		if (leaving) return;
		// `to === null` — уход из приложения (закрытие вкладки, внешняя ссылка),
		// willUnload — полная перезагрузка документа. Там работает нативный
		// диалог браузера, наша модалка отрисоваться уже не успеет.
		const to = nav.to;
		if (!to || nav.willUnload) return;
		const hasDraft = get(singboxRouter.staging)?.hasDraft ?? false;
		if (!remindAboutDraft(nav.from?.url.pathname, to.url.pathname, hasDraft)) return;
		nav.cancel();
		leavingTo = to.url.href;
	});

	function confirmLeave(): void {
		const href = leavingTo;
		leavingTo = null;
		if (!href) return;
		leaving = true;
		void goto(href).finally(() => {
			leaving = false;
		});
	}
</script>

{#if singboxMissing}
	<PageContainer>
		<EmptyState
			title="Sing-box не установлен"
			description="Маршрутизация sing-box доступна после установки пакета — откройте «Настройки»."
		/>
	</PageContainer>
{:else}
	<!-- Гуттер только по горизонтали: без черновика StagingBanner не рисует
	     ничего, и вертикальный отступ дал бы пустую полосу над каждой страницей
	     группы. Отступ до контента даёт верхний гуттер PageContainer страницы. -->
	<div class="engine-staging">
		<StagingBanner />
	</div>
	{@render children()}
{/if}

<ModeSwitchHost />

<SelectiveRebuildModal
	open={rebuildOpen}
	progress={$selectiveProgress}
	onMinimize={minimizeRebuild}
	onDismiss={dismissRebuild}
/>

<Modal
	open={leavingTo !== null}
	title="Правки не применены"
	size="sm"
	onclose={() => (leavingTo = null)}
>
	<p>Правки sing-box сохранены как черновик, но <strong>ещё не применены</strong>. Черновик никуда не денется, но маршрутизация не изменится, пока вы не нажмёте «Применить».</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (leavingTo = null)}>Остаться</Button>
		<Button variant="primary" size="md" onclick={confirmLeave}>Уйти всё равно</Button>
	{/snippet}
</Modal>

<style>
	.engine-staging {
		padding-inline: var(--layout-gutter-x);
	}

	/* Свой отступ снизу баннеру здесь не нужен — его роль играет верхний гуттер
	   PageContainer. Правило :global: scoped-CSS не достаёт до разметки дочернего
	   компонента. */
	.engine-staging :global(.staging-inline) {
		margin-bottom: 0;
		margin-top: var(--layout-gutter-y-top);
	}
</style>
