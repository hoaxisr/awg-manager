<script lang="ts">
	// Страница «Движок» — состояние и настройки движка sing-box по разделу 3
	// спеки docs/superpowers/specs/2026-08-04-nav-v3-5d-singbox-design.md.
	//
	// Готово: шапка (состояние, режим, управление), стат-полоса, «Захват
	// трафика» (режим + настройки активного режима), «Селективный перехват» и
	// «Исключения». Карточки «Здоровье», «QoS» и «Конфигурация» наполняют
	// задачи 6-7 той же волны.
	//
	// Общие механики группы — в (engine)/+layout.svelte: ModeSwitchHost, баннер
	// черновика, окно пересборки ipset, гейт «Sing-box не установлен» и
	// напоминание о непринятом черновике. Статус и настройки движка праймит
	// /sb/+layout.svelte, живые соединения биндит он же — страница ничего из
	// этого не заводит повторно. ИСКЛЮЧЕНИЕ: статус селективного обхода — его до
	// nav-v3 грузила только шторка sb-router, и прайминг заводит сама карточка
	// «Селективный перехват» (там же и объяснение).
	import { PageContainer } from '$lib/components/layout';
	import {
		EngineCaptureCard,
		EngineExclusionsCard,
		EngineHeader,
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
</PageContainer>
