<script lang="ts">
	// Карточка «Захват трафика» страницы «Движок» (раздел 3 спеки nav-v3 5D):
	// название режима с однострочным объяснением, техническая подпись, кнопка
	// «Сменить режим» и настройки ТОЛЬКО активного режима.
	//
	// ПОЧЕМУ ТОЛЬКО АКТИВНОГО. Раздел 8 спеки записывает правку конфигурации
	// неактивного режима в осознанные потери: три набора полей на одной карточке
	// требовали бы от пользователя знать, какие из них сейчас что-то значат.
	//
	// РЕЖИМ БЕРЁТСЯ ИЗ НАСТРОЕК, а не из статуса, и нормализуется тем же
	// `normalizeRoutingMode`, что и пилюля шапки: при выключенном движке оба
	// говорят про persisted-режим — «в этом режиме движок работал и включится».
	// Своего (локального) выбора карточка не заводит, см. ModePickerModal.
	//
	// Настройки сохраняются общим для sb-router пайплайном
	// `mergeAndSaveSettings` (свежий GET → merge → PUT → loadAll): PUT уносит
	// полный объект, и эхо устаревшего стора молча откатывало бы чужие правки.
	import { Button, Card } from '$lib/components/ui';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { modeSwitch, modeSwitchBusy } from '$lib/stores/modeSwitch';
	import { notifications } from '$lib/stores/notifications';
	import { mergeAndSaveSettings } from '$lib/components/sb-router/settingsActions';
	import TrafficSourceSettings from '$lib/components/sb-router/TrafficSourceSettings.svelte';
	import PolicyTunCard from '$lib/components/sb-router/PolicyTunCard.svelte';
	import FakeIpCaptureSettings from './FakeIpCaptureSettings.svelte';
	import ModePickerModal from './ModePickerModal.svelte';
	import { captureModeCopy } from './captureModes';
	import { normalizeRoutingMode, type EngineRoutingMode } from './engineRunState';
	import type { SingboxRouterSettings } from '$lib/types';

	const status = singboxRouter.status;
	const settings = singboxRouter.settings;

	const cfg = $derived($settings);
	const enabled = $derived($status?.enabled ?? false);
	const mode = $derived(normalizeRoutingMode($settings?.routingMode));
	const copy = $derived(captureModeCopy(mode));
	const switchBusy = $derived(modeSwitchBusy($modeSwitch));

	let pickerOpen = $state(false);

	function pick(target: EngineRoutingMode): void {
		pickerOpen = false;
		// Совпадение с текущим режимом стор гасит сам (no-op в request), поэтому
		// отдельной ветки «выбрали то же самое» здесь нет — иначе условие жило бы
		// в двух местах. Подтверждение и прогресс рисует ModeSwitchHost в layout.
		modeSwitch.request(target);
	}

	async function applyPatch(patch: Partial<SingboxRouterSettings>): Promise<void> {
		try {
			await mergeAndSaveSettings(patch);
		} catch (e) {
			notifications.error(
				`Не удалось сохранить: ${e instanceof Error ? e.message : String(e)}`,
			);
		}
	}
</script>

<!-- Шапка карточки — своя, а не сниппет `header` компонента Card: там жёсткие
     align-items: center и nowrap, и на 390px кнопка вставала посреди трёх строк
     объяснения. Здесь строка переносится, и кнопка уходит под текст. -->
<Card padding="none">
	<div class="head-row">
		<div class="head">
			<div class="title">Захват трафика</div>
			<div class="mode">
				<span class="mode-name">{copy.label}</span>
				<span class="mode-sum">{copy.summary}</span>
			</div>
		</div>
		<Button
			variant="secondary"
			size="sm"
			disabled={switchBusy}
			title={switchBusy ? 'Смена режима уже идёт' : 'Выбрать другой режим захвата'}
			onclick={() => (pickerOpen = true)}
		>
			Сменить режим
		</Button>
	</div>

	<p class="tech">{copy.tech}</p>

	{#if !cfg}
		<p class="empty">Настройки движка ещё не загружены.</p>
	{:else if mode === 'tproxy'}
		<TrafficSourceSettings
			{cfg}
			deviceCount={$status?.deviceCount ?? 0}
			policyExists={$status?.policyExists !== false}
			onPatch={(patch) => void applyPatch(patch)}
		/>
	{:else if mode === 'policy-tun'}
		<PolicyTunCard {cfg} status={$status} onPatch={(patch) => applyPatch(patch)} />
	{:else}
		<FakeIpCaptureSettings {cfg} status={$status} onPatch={(patch) => applyPatch(patch)} />
	{/if}
</Card>

<ModePickerModal
	open={pickerOpen}
	current={mode}
	{enabled}
	onpick={pick}
	onclose={() => (pickerOpen = false)}
/>

<style>
	.head-row {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		justify-content: space-between;
		gap: 8px;
		padding: 12px var(--sp-4);
		border-bottom: 1px solid var(--border);
	}

	.head {
		display: flex;
		flex-direction: column;
		gap: 2px;
		/* Текст обязан ужиматься до переноса строки, иначе на 390px кнопка
		   выдавливается за край карточки. */
		flex: 1 1 260px;
		min-width: 0;
	}

	.title {
		font-size: 15px;
		font-weight: 700;
		color: var(--text-primary);
	}

	.mode {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 6px;
		font-size: 12px;
		line-height: 1.4;
	}

	.mode-name {
		font-weight: 600;
		color: var(--accent);
	}

	.mode-sum {
		color: var(--text-muted);
	}

	/* Техническая подпись — моноширинная, как чипы режима в макете. */
	.tech {
		margin: 0;
		padding: 10px var(--sp-4);
		border-bottom: 1px solid var(--border);
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--text-muted);
		word-break: break-word;
	}

	.empty {
		margin: 0;
		padding: 14px var(--sp-4);
		font-size: 12px;
		color: var(--text-muted);
	}
</style>
