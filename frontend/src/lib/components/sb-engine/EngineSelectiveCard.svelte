<script lang="ts">
	// Карточка «Селективный перехват» страницы «Движок» (issue #564/#567).
	// Источник — StatusDrawer.svelte:225-307, 481-573.
	//
	// ГЕЙТ: ТОЛЬКО TPROXY. В шторке условие было `!policyTunMode`, но шторка
	// знала ровно два режима (StatusDrawer:64-77) — fakeip-tun туда не попадал в
	// принципе. На единой странице тот же `!policyTunMode` дал бы для FakeIP
	// `false` и показал бы чисто TPROXY-механику как живую. Селективный ipset —
	// это `SelectiveIPSet: sr.SelectiveBypass` в RestoreInputSpec, то есть
	// netfilter-правила TPROXY-перехвата (service_lifecycle.go:711-792); в
	// policy-tun и fakeip-tun трафик заводит не netfilter, обходить нечего.
	//
	// ПРАЙМИНГ СТАТУСА — ЗДЕСЬ. До nav-v3 статус селективного обхода грузил
	// ровно один вызов: StatusDrawer.onMount → loadSelectiveStatus
	// (StatusDrawer:157, 248-253). Шторки больше нет, и без загрузки отсюда
	// карточка была бы пустой навсегда. Дальше состояние держит SSE
	// singbox-router:selective-status (routes/+layout.svelte).
	import { get } from 'svelte/store';
	import { Button, Card, Modal, Toggle } from '$lib/components/ui';
	import { TriangleAlert } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { selectiveBypass } from '$lib/stores/selectiveBypass';
	import { triggerSelectiveRebuildManual } from '$lib/components/sb-router/selectiveRebuildTrigger';
	import SelectiveIpsetSnapshot from '$lib/components/sb-router/SelectiveIpsetSnapshot.svelte';
	import { applyEngineSettings } from './engineSettings';
	import { normalizeRoutingMode } from './engineRunState';

	const routerStatus = singboxRouter.status;
	const routerSettings = singboxRouter.settings;
	const { status: selectiveStatusStore } = selectiveBypass;

	const cfg = $derived($routerSettings);
	const mode = $derived(normalizeRoutingMode($routerSettings?.routingMode));
	const isTproxy = $derived(mode === 'tproxy');

	// route.final = direct — обязательное условие: при catch-all проксировании
	// весь трафик и так идёт через sing-box, селективный ipset не имеет смысла.
	const routeFinal = $derived($routerStatus?.final || 'direct');
	const finalOk = $derived(routeFinal === 'direct');

	const selectiveStatus = $derived($selectiveStatusStore);
	const statusLoaded = $derived(selectiveStatus !== null);
	const ipsetOk = $derived(selectiveStatus?.available ?? false);
	const enabled = $derived(cfg?.selectiveBypass ?? false);

	// Пересборка в процессе: локальный флаг покрывает HTTP round-trip (202),
	// status.rebuilding — фон после ответа и «страница открыта во время
	// пересборки». Сбрасывается по SSE selective-status с rebuilding: false.
	let rebuilding = $state(false);
	const rebuildInFlight = $derived(rebuilding || (selectiveStatus?.rebuilding ?? false));

	let installing = $state(false);
	let snapshotOpen = $state(false);

	const snapshot = $derived(selectiveStatus?.snapshot ?? null);
	const hasSnapshot = $derived(
		!!snapshot &&
			((snapshot.entryCount ?? 0) > 0 ||
				(snapshot.domainMatcherCount ?? snapshot.domainResults?.length ?? 0) > 0 ||
				(snapshot.staticCidrCount ?? snapshot.staticCidrs?.length ?? 0) > 0),
	);

	const lastRebuildLabel = $derived(
		selectiveStatus?.lastRebuild ? new Date(selectiveStatus.lastRebuild).toLocaleString() : '—',
	);

	// Прайминг ровно один раз за жизнь компонента и только когда карточка
	// действительно на экране: в policy-tun и FakeIP её нет, и запрос был бы
	// сетевым шумом. `get` вместо `$` — проверка «уже загружено» не должна
	// становиться зависимостью эффекта, иначе приход статуса перезапускал бы его.
	let primed = false;
	$effect(() => {
		if (!isTproxy || primed) return;
		primed = true;
		if (get(selectiveStatusStore) !== null) return;
		void loadSelectiveStatus();
	});

	async function loadSelectiveStatus(): Promise<void> {
		try {
			selectiveBypass.applyStatus(await api.singboxRouterSelectiveStatus());
		} catch {
			// Статус — необязательные данные: без него карточка показывает
			// описание, а тумблер остаётся доступным. Тост тут был бы шумом на
			// каждом входе на страницу с недоступной ручкой.
		}
	}

	async function installDeps(): Promise<void> {
		installing = true;
		try {
			selectiveBypass.applyStatus(await api.singboxRouterSelectiveInstallDeps());
		} catch (e) {
			notifications.error(
				'Не удалось установить ipset: ' + (e instanceof Error ? e.message : String(e)),
			);
		} finally {
			installing = false;
		}
	}

	async function rebuild(): Promise<void> {
		rebuilding = true;
		try {
			await triggerSelectiveRebuildManual();
		} finally {
			rebuilding = false;
		}
	}

	function toggle(checked: boolean): void {
		if (checked && !finalOk) {
			notifications.error('Селективный перехват требует route.final = direct');
			return;
		}
		void applyEngineSettings({ selectiveBypass: checked });
	}
</script>

{#if isTproxy}
	<Card padding="none">
		<div class="head-row">
			<div class="head">
				<div class="title">Селективный перехват</div>
				<div class="sub">Только трафик из правил заходит в sing-box</div>
			</div>
			<Toggle
				checked={enabled}
				ariaLabel="Селективный перехват"
				disabled={!finalOk || (statusLoaded && !ipsetOk)}
				onchange={toggle}
			/>
		</div>

		<div class="body">
			{#if !finalOk}
				<p class="hint warn">
					<TriangleAlert size={13} />
					<span>
						Несовместимо с route.final = «{routeFinal}»: при catch-all проксировании весь трафик
						идёт через sing-box, селективный ipset не имеет смысла. Установите final в «direct» в
						разделе маршрутизации.
					</span>
				</p>
			{:else if !statusLoaded}
				<!-- Статус ещё грузится: описание показываем, тумблер не блокируем. -->
				<p class="hint">
					При включении в sing-box попадает только трафик к целевым IP из правил маршрутизации
					(proxy). Весь остальной трафик полностью обходит sing-box — не только VPN, а сам движок —
					и идёт напрямую в WAN. Так соединения стабильнее и предсказуемее: игры, стриминг и
					локальный трафик не проходят через прокси-цепочку.
				</p>
			{:else if !ipsetOk}
				<p class="hint warn">
					<TriangleAlert size={13} />
					<span>Требуется пакет <code class="mono">ipset</code> — он не установлен на роутере.</span>
				</p>
				<Button variant="ghost" size="sm" fullWidth loading={installing} onclick={installDeps}>
					{installing ? 'Установка…' : 'Установить ipset'}
				</Button>
			{:else if enabled}
				<p class="hint">
					В sing-box попадает только трафик к IP из правил proxy; остальное полностью обходит движок
					и уходит в WAN напрямую — стабильнее для игр, стриминга и локального трафика.
				</p>

				<div class="stats">
					<div class="stat-line">
						<span class="stat-label">Записей в ipset</span>
						<span class="stat-value">{selectiveStatus?.entryCount ?? 0}</span>
					</div>
					<div class="stat-line">
						<span class="stat-label">Последняя пересборка</span>
						<span class="stat-value">{lastRebuildLabel}</span>
					</div>
				</div>

				{#if selectiveStatus?.lastError}
					<p class="hint warn">
						<TriangleAlert size={13} />
						<span>Ошибка пересборки: {selectiveStatus.lastError}</span>
					</p>
				{/if}

				<p class="hint">
					После смены правил нажмите «Применить» — ipset пересоберётся автоматически. Уже открытые
					соединения (вкладки браузера) могут идти по старому пути, пока не закроются — откройте
					сайт в новой вкладке для проверки.
				</p>
				<p class="hint">
					<!-- Именно /logs (корзина app), а НЕ /sb/logs: пересборку пишет appLog
					     (service_selective.go, selective/builder.go), а «Журнал движка»
					     показывает корзину singbox — stdout самого sing-box. Имена группы и
					     подгруппы сверены с LogsToolbar (routing → «Маршрутизация»,
					     selective → «Селективный ipset»). -->
					Подробный лог резолва: <a href="/logs">Журнал</a>, группа «Маршрутизация», подгруппа
					«Селективный ipset».
				</p>

				<div class="actions">
					{#if hasSnapshot}
						<Button variant="ghost" size="sm" fullWidth onclick={() => (snapshotOpen = true)}>
							Содержимое ipset (домены → IP)
						</Button>
					{/if}
					<Button
						variant="ghost"
						size="sm"
						fullWidth
						loading={rebuildInFlight}
						onclick={rebuild}
					>
						{rebuildInFlight ? 'Пересборка…' : 'Пересобрать ipset'}
					</Button>
				</div>
			{:else}
				<p class="hint">
					При включении в sing-box попадает только трафик к целевым IP из правил маршрутизации
					(proxy). Весь остальной трафик полностью обходит sing-box — не только VPN, а сам движок —
					и идёт напрямую в WAN. Так соединения стабильнее и предсказуемее: игры, стриминг и
					локальный трафик не проходят через прокси-цепочку.
				</p>
			{/if}
		</div>
	</Card>

	<!-- Окно прогресса пересборки рисует SelectiveRebuildModal из layout группы;
	     здесь только просмотр СОДЕРЖИМОГО уже собранного ipset. -->
	<Modal open={snapshotOpen} title="Содержимое ipset" size="lg" onclose={() => (snapshotOpen = false)}>
		{#if snapshot}
			<SelectiveIpsetSnapshot {snapshot} />
		{/if}
	</Modal>
{/if}

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
		/* Текст обязан ужиматься до переноса, иначе на 390px тумблер
		   выдавливается за край карточки. */
		flex: 1 1 220px;
		min-width: 0;
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
	.body {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 14px var(--sp-4);
	}
	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}
	.hint a {
		color: var(--accent);
	}
	/* Flex только у строк с иконкой: в обычной подсказке он разнёс бы ссылку и
	   запятую после неё на gap. */
	.hint.warn {
		display: flex;
		align-items: flex-start;
		gap: 5px;
		color: var(--color-warning, #dab856);
	}
	.hint.warn :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
	}
	.stats {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
	}
	.stat-line {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		font-size: 12px;
	}
	.stat-label {
		color: var(--text-muted);
	}
	.stat-value {
		color: var(--text-secondary);
		font-family: var(--font-mono);
		font-size: 11.5px;
		text-align: right;
	}
	.actions {
		display: flex;
		flex-direction: column;
		gap: 6px;
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
