<script lang="ts">
	// Страница «Движок» — состояние и настройки движка sing-box по разделу 3
	// спеки docs/superpowers/specs/2026-08-04-nav-v3-5d-singbox-design.md.
	//
	// Пустой каркас: волна 5D2a вынесла отсюда временное содержимое старой
	// /sb/routing (обе поверхности, TProxy и FakeIP, с переключателем ?view=),
	// наполняют страницу задачи 3-7 той же волны. До них живых редакторов
	// правил, DNS, наборов, групп и inbounds в приложении нет — осознанная цена
	// того, что каждая следующая задача строит настоящую страницу, а не рисует
	// поверх старой.
	//
	// Общие механики группы — в (engine)/+layout.svelte: ModeSwitchHost, баннер
	// черновика, окно пересборки ipset, гейт «Sing-box не установлен» и
	// напоминание о непринятом черновике. Статус и настройки движка праймит
	// /sb/+layout.svelte.
	import { PageContainer, PageHeader } from '$lib/components/layout';
</script>

<svelte:head>
	<title>Движок - AWGM</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Движок" />
</PageContainer>
