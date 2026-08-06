<script lang="ts">
	// Страница «Движок» — состояние и настройки движка sing-box по разделу 3
	// спеки docs/superpowers/specs/2026-08-04-nav-v3-5d-singbox-design.md.
	//
	// Готово: шапка (состояние, режим, управление), стат-полоса и «Захват
	// трафика» (режим + настройки активного режима). Карточки «Селективный
	// перехват», «Исключения», «Здоровье», «QoS» и «Конфигурация» наполняют
	// задачи 5-7 той же волны.
	//
	// Общие механики группы — в (engine)/+layout.svelte: ModeSwitchHost, баннер
	// черновика, окно пересборки ipset, гейт «Sing-box не установлен» и
	// напоминание о непринятом черновике. Статус и настройки движка праймит
	// /sb/+layout.svelte, живые соединения биндит он же — страница ничего из
	// этого не заводит повторно.
	import { PageContainer } from '$lib/components/layout';
	import { EngineCaptureCard, EngineHeader, EngineStatStrip } from '$lib/components/sb-engine';
</script>

<svelte:head>
	<title>Движок - AWGM</title>
</svelte:head>

<PageContainer>
	<EngineHeader />
	<EngineStatStrip />
	<EngineCaptureCard />
</PageContainer>
