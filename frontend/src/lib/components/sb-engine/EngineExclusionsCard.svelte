<script lang="ts">
	// Карточка «Исключения» страницы «Движок». Источник —
	// StatusDrawer.svelte:584-606, но там она была одним куском и без режимного
	// гейта: одинаково предлагала порты, подсети и пресеты во всех режимах, а
	// работают они по-разному (см. exclusionsScope.ts — разбор по коду бэкенда).
	//
	// ПОЧЕМУ РАЗДЕЛЕНА, А НЕ СПРЯТАНА. Пресет keendns — DNS-перезапись, а не
	// netfilter: он работает во всех трёх режимах. Спрятать карточку в FakeIP
	// значило бы унести вместе с враньём работающую настройку. Поэтому блоков
	// два — по механизму, и честная подпись висит только на том, который в
	// текущем режиме ничего не делает.
	//
	// ПОЧЕМУ НЕ БЛОКИРУЕМ ПОЛЯ ТАМ, ГДЕ ОНИ НЕ РАБОТАЮТ. Настройка персистится и
	// оживает после возврата на TPROXY; заблокированный ввод отнял бы
	// возможность подготовить исключения заранее, ничего не объяснив взамен.
	// Обязанность «пользователь видит, что настройка ни на что не влияет»
	// закрывает предупреждение над блоком.
	import { Card } from '$lib/components/ui';
	import { TriangleAlert } from 'lucide-svelte';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { BYPASS_PRESETS } from '$lib/components/sb-router/settingsActions';
	import PortChipsInput from '$lib/components/sb-router/PortChipsInput.svelte';
	import SubnetChipsInput from '$lib/components/sb-router/SubnetChipsInput.svelte';
	import { applyEngineSettings } from './engineSettings';
	import { normalizeRoutingMode } from './engineRunState';
	import { netfilterExclusionsScope, partitionBypassPresets } from './exclusionsScope';

	const routerSettings = singboxRouter.settings;

	const cfg = $derived($routerSettings);
	const mode = $derived(normalizeRoutingMode($routerSettings?.routingMode));
	// Считаем только ВКЛЮЧЁННЫЕ классы: выключенный класс не попадает в qosSpecs
	// бэкенда, значит и netfilter-цепочку из-за него не поставят.
	const qosClassCount = $derived((cfg?.qosClasses ?? []).filter((c) => c.enabled).length);
	const scope = $derived(netfilterExclusionsScope(mode, qosClassCount));

	const presets = partitionBypassPresets(BYPASS_PRESETS);
	const active = $derived(cfg?.bypassPresets ?? []);

	function togglePreset(id: string): void {
		const current = cfg?.bypassPresets ?? [];
		const next = current.includes(id) ? current.filter((x) => x !== id) : [...current, id];
		void applyEngineSettings({ bypassPresets: next });
	}
</script>

<Card padding="none">
	<div class="head-row">
		<div class="title">Исключения</div>
		<div class="sub">Что не должно проходить через sing-box</div>
	</div>

	{#if !cfg}
		<p class="empty">Настройки движка ещё не загружены.</p>
	{:else}
		<section class="sec">
			<div class="sec-cap">Перезапись DNS</div>
			<div class="chips">
				{#each presets.dns as p (p.id)}
					<button
						type="button"
						class="chip"
						class:active={active.includes(p.id)}
						aria-pressed={active.includes(p.id)}
						onclick={() => togglePreset(p.id)}
					>
						<span class="chip-label">{p.label}</span>
						<span class="chip-desc">{p.desc}</span>
					</button>
				{/each}
			</div>
			<p class="hint">
				Не порты и не подсети: свой домен KeenDNS/CrazeDNS резолвится в LAN-адрес роутера.
				Работает во всех режимах захвата.
			</p>
		</section>

		<section class="sec">
			<div class="sec-cap">Прямо в WAN (netfilter)</div>

			{#if scope.notice}
				<p class="hint" class:warn={!scope.applies}>
					{#if !scope.applies}<TriangleAlert size={13} />{/if}
					<span>{scope.notice}</span>
				</p>
			{/if}

			<div class="chips">
				{#each presets.netfilter as p (p.id)}
					<button
						type="button"
						class="chip"
						class:active={active.includes(p.id)}
						aria-pressed={active.includes(p.id)}
						onclick={() => togglePreset(p.id)}
					>
						<span class="chip-label">{p.label}</span>
						<span class="chip-desc">{p.desc}</span>
					</button>
				{/each}
			</div>

			<div class="field">
				<label class="lbl" for="eng-ports-input">Доп. порты</label>
				<PortChipsInput
					inputId="eng-ports-input"
					value={cfg.bypassExtraPorts ?? ''}
					onChange={(v) => void applyEngineSettings({ bypassExtraPorts: v })}
				/>
			</div>
			<p class="hint">
				Эти порты пойдут мимо sing-box (прямо в WAN). Полезно для L2TP/NTP/SMB не ломая
				LAN-сервисы. Поддерживаются одиночные порты (<code class="mono">443 TCP</code>) и диапазоны
				(<code class="mono">5000-5500 UDP</code>).
			</p>

			<div class="field">
				<label class="lbl" for="eng-subnets-input">Доп. подсети</label>
				<SubnetChipsInput
					inputId="eng-subnets-input"
					value={cfg.bypassExtraSubnets ?? ''}
					onChange={(v) => void applyEngineSettings({ bypassExtraSubnets: v })}
				/>
			</div>
			<p class="hint">
				IP или подсети, чей трафик целиком пойдёт мимо sing-box (прямо в WAN). Нужно для
				корпоративных VPN (Cisco AnyConnect и т.п.), чтобы их трафик не перехватывался.
			</p>
		</section>
	{/if}
</Card>

<style>
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
	.sec {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 14px var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	.sec:last-of-type {
		border-bottom: 0;
	}
	.sec-cap {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.lbl {
		font-size: 11px;
		color: var(--text-muted);
		font-weight: 500;
	}
	/* Поток текста, а НЕ flex: во flex-контейнере <code> с примером порта
	   становится отдельным элементом, и на 390px подсказка разваливалась —
	   «443 TCP» и «5000-5500 UDP» вставали колонками, а текст обтекал их.
	   Flex включается только у строки с иконкой предупреждения. */
	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}
	.hint.warn {
		display: flex;
		align-items: flex-start;
		gap: 5px;
		color: var(--color-warning, #dab856);
	}
	.hint :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
	}
	.chips {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.chip {
		text-align: left;
		/* align-items приходит из глобального сброса кнопок как center: в узкой
		   шторке это было незаметно, а на широкой карточке подписи чипов
		   выстроились по центру. */
		align-items: flex-start;
		padding: 8px 10px;
		border-radius: var(--radius-sm);
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		cursor: pointer;
		font-family: inherit;
		color: inherit;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.chip.active {
		background: var(--accent-soft);
		border-color: var(--accent);
	}
	.chip-label {
		font-size: 12.5px;
		font-weight: 600;
	}
	.chip-desc {
		font-size: 11px;
		color: var(--text-muted);
		font-family: var(--font-mono);
	}
	.empty {
		margin: 0;
		padding: 14px var(--sp-4);
		font-size: 12px;
		color: var(--text-muted);
	}
	code.mono {
		font-family: var(--font-mono);
		font-size: 10.5px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 3px;
		padding: 0 3px;
		color: var(--text-secondary);
	}
</style>
