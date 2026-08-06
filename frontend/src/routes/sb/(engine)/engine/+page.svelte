<script lang="ts">
	// Страница «Движок» — состояние и настройки движка sing-box по разделу 3
	// спеки docs/superpowers/specs/2026-08-04-nav-v3-5d-singbox-design.md.
	//
	// Готово: шапка (состояние, режим, управление), стат-полоса, «Захват
	// трафика» (режим + настройки активного режима), «Селективный перехват»,
	// «Исключения», «Здоровье» и «QoS · приоритеты». Карточку «Конфигурация»
	// наполняет задача 7 той же волны.
	//
	// Общие механики группы — в (engine)/+layout.svelte: ModeSwitchHost, баннер
	// черновика, окно пересборки ipset, гейт «Sing-box не установлен» и
	// напоминание о непринятом черновике. Статус и настройки движка праймит
	// /sb/+layout.svelte, живые соединения биндит он же — страница ничего из
	// этого не заводит повторно. ИСКЛЮЧЕНИЯ, где прайминг заводит сама карточка
	// (и там же объяснение, почему): статус селективного обхода —
	// «Селективный перехват», список WAN-интерфейсов — «Здоровье», каталог
	// outbound'ов — «QoS · приоритеты».
	import { PageContainer } from '$lib/components/layout';
	import {
		EngineCaptureCard,
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
</PageContainer>
