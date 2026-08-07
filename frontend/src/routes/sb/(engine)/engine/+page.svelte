<script lang="ts">
	// Страница «Движок» — состояние и настройки движка sing-box по разделу 3
	// спеки docs/superpowers/specs/2026-08-04-nav-v3-5d-singbox-design.md.
	//
	// Общие механики группы — в (engine)/+layout.svelte: ModeSwitchHost, баннер
	// черновика, окно пересборки ipset, гейт «Sing-box не установлен» и
	// напоминание о непринятом черновике.
	//
	// ПРАЙМИНГ. Правило 5D1: layout грузит то, что нужно бейджам сайдбара
	// (статус и настройки — /sb/+layout.svelte, живые соединения — он же,
	// черновик — (engine)/+layout.svelte), страница праймит то, что показывает
	// сама. Полного `singboxRouter.loadAll()` здесь НЕТ намеренно: правила,
	// наборы, DNS и пресеты страница не показывает, а десять ручек на вход
	// стоили бы ровно того, от чего 5D0 отказалась.
	//
	// Прайминг заводят сами карточки — тем самым он умирает вместе с ними и не
	// уходит в сеть за данными, которых на экране нет:
	//
	//   * статус селективного обхода — «Селективный перехват» (только tproxy);
	//   * список WAN-интерфейсов — «Здоровье» (только при ручном выборе);
	//   * каталог outbound'ов — «QoS · приоритеты» (кроме fakeip);
	//   * слоты config.d — «Конфигурация».
	//
	// Всё остальное приходит само и повторной загрузки не требует: память и
	// трафик движка — SSE (routes/+layout.svelte), живые соединения — Clash-WS,
	// версия sing-box и сводка системы — polling-сторы, каталоги туннелей, AWG и
	// подписок для дропдаунов QoS — их же polling по первой подписке
	// (`singboxRouter.options` — derived, а не запрос).
	import { PageContainer } from '$lib/components/layout';
	import {
		EngineCaptureCard,
		EngineConfigCard,
		EngineExclusionsCard,
		EngineHeader,
		EngineHealthCard,
		EngineQosCard,
		EngineSelectiveCard,
		EngineStatStrip,
	} from '$lib/components/sb-engine';
</script>

<svelte:head>
	<title>Движок - AWGM</title>
</svelte:head>

<PageContainer>
	<EngineHeader />
	<EngineStatStrip />
	<EngineCaptureCard />
	<EngineSelectiveCard />
	<EngineExclusionsCard />
	<EngineHealthCard />
	<EngineQosCard />
	<EngineConfigCard />
</PageContainer>
