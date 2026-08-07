<script lang="ts">
	// Карточка «QoS · приоритеты» страницы «Движок» — проводка `QosSettingsCard`
	// к сторам страницы. Сама таблица классов переносится КАК ЕСТЬ: у неё есть
	// оптимистичный черновик, очередь сохранений и разбор пропавшего outbound'а,
	// и переписывать всё это ради нового каркаса нечего.
	//
	// РЕЖИМ FakeIP. Спека требует «вместо карточки — строка-объяснение». Внутри
	// `QosSettingsCard` такая ветка уже есть (`locked` по routingMode), поэтому
	// гейт РЕНДЕРА снаружи не заводим: два места, решающих один вопрос,
	// разъезжаются. Строка при этом остаётся в карточке с заголовком — иначе
	// объяснение «почему тут пусто» висело бы в воздухе без названия того, чего
	// нет.
	//
	// Гейт ПРАЙМИНГА (ниже) — вопрос тот же, поэтому предикат общий с
	// `QosSettingsCard`: разъехавшись, они дали бы пустой дропдаун ровно там,
	// ради чего прайминг и заводили.
	//
	// КАТАЛОГ OUTBOUND'ОВ ГРУЗИТ ЭТА КАРТОЧКА. `singboxRouter.options` собирается
	// из стора outbounds, а его на страницах движка не грузил никто: полный
	// loadAll звала шторка sb-router, которой больше нет. Без прайминга дропдаун
	// класса пуст, и любой существующий класс выглядел бы как «outbound не
	// найден». Запрос один (list-outbounds), только когда карточка не заперта
	// режимом и стор пуст.
	import { get } from 'svelte/store';
	import { Card } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import QosSettingsCard from '$lib/components/sb-router/QosSettingsCard.svelte';
	import { isQosLockedByMode } from '$lib/components/sb-router/qosClasses';
	import { applyEngineSettings } from './engineSettings';
	import type { SingboxRouterSettings } from '$lib/types';

	const routerStatus = singboxRouter.status;
	const routerSettings = singboxRouter.settings;
	const routerOptions = singboxRouter.options;

	const cfg = $derived($routerSettings);
	const locked = $derived(isQosLockedByMode($routerSettings?.routingMode));

	let outboundsFailed = $state(false);
	// Обычный `let`, а НЕ `$state`: латч «запрос уже уходил». Сделай его
	// реактивным и сбрось в catch — эффект перезапустится сам и будет долбить
	// упавшую ручку без остановки. Повтор — только по кнопке.
	let outboundsRequested = false;
	$effect(() => {
		if (locked || outboundsRequested) return;
		// get(), а не $-подписка: наличие outbound'ов не должно становиться
		// зависимостью эффекта — иначе он перезапускался бы на каждой их правке.
		if (get(singboxRouter.outbounds).length > 0) return;
		outboundsRequested = true;
		void loadOutbounds();
	});

	async function loadOutbounds(): Promise<void> {
		try {
			singboxRouter.applyOutbounds(await api.singboxRouterListOutbounds());
			outboundsFailed = false;
		} catch (_e) {
			// Молчать нельзя: с пустым каталогом КАЖДЫЙ настроенный класс
			// рендерится как «outbound не найден», и пользователь идёт
			// переназначать рабочие классы.
			outboundsFailed = true;
		}
	}

	// QosSettingsCard сериализует свои PUT-ы по возвращённому промису: очередь
	// ждёт именно его, поэтому промис обязан дожить до вызывающего.
	async function patch(next: Partial<SingboxRouterSettings>): Promise<void> {
		await applyEngineSettings(next);
	}
</script>

{#if cfg}
	<Card padding="none">
		<div class="head-row">
			<div class="title">QoS · приоритеты</div>
			<div class="sub">Помеченный DSCP трафик уходит в свой туннель мимо остальных правил</div>
		</div>
		{#if outboundsFailed}
			<p class="load-error">
				Не удалось загрузить каталог выходов — существующие классы покажутся как «outbound не
				найден».
				<button type="button" class="retry" onclick={() => void loadOutbounds()}>
					Повторить
				</button>
			</p>
		{/if}
		<QosSettingsCard
			{cfg}
			status={$routerStatus}
			outboundOptions={$routerOptions}
			onPatch={patch}
		/>
	</Card>
{/if}

<style>
	/* Шапка — та же идиома, что у соседних карточек страницы (своя, а не сниппет
	   `header` компонента Card: там жёсткие align-items: center и nowrap). */
	.head-row {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 12px var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	.title {
		font-size: 15px;
		font-weight: 700;
		color: var(--text-primary);
	}
	.sub {
		font-size: 12px;
		line-height: 1.4;
		color: var(--text-muted);
	}
	/* Отказ прайминга: поток текста, кнопка внутри строки. */
	.load-error {
		margin: 0;
		padding: 10px var(--sp-4) 0;
		font-size: 11.5px;
		line-height: 1.4;
		color: var(--color-error, #d05b5b);
	}
	.retry {
		padding: 0;
		border: 0;
		background: none;
		font: inherit;
		color: var(--color-accent, #5b9bd0);
		text-decoration: underline;
		cursor: pointer;
	}
</style>
