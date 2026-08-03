<!--
  Карточка «HTTP-сервер»: порт и интерфейсы, на которых слушает веб-интерфейс
  awg-manager. Применение ЖИВОЕ (без рестарта демона) с confirm-or-revert:

    1. POST /server/listen/change — бэкенд make-before-break перебиндивает
       листенеры и взводит откат (~2 мин); настройки ещё НЕ сохранены.
    2. Фронт уходит на новый origin, унося одноразовый токен в URL-фрагменте
       (#listen-confirm=...) — cookie сессии привязана к хосту и смену
       интерфейса не переживает, токен аутентифицирует confirm сам.
    3. Эта же карточка на новой странице настроек в onMount видит фрагмент,
       зовёт POST /server/listen/confirm — бэкенд гасит откат и персистит.
       Не дошли (адрес недоступен) — бэкенд сам откатывается к старому.

  Листенер 127.0.0.1 держится всегда (реверс-прокси NDMS, health-пробы,
  спасательный люк) — потому loopback в списке не предлагается.
  Пустой список интерфейсов = все (0.0.0.0).
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, ConfirmModal, ChipMultiSelect, type ChipOption } from '$lib/components/ui';
	import type { RouterInterface, ServerListenState } from '$lib/types';


	let listen = $state<ServerListenState | null>(null);
	let ifaces = $state<RouterInterface[]>([]);
	// number|string: Svelte на <input type="number"> присваивает в bind число,
	// а начальное/пустое значение — строка; никаких строковых методов ниже.
	let portDraft = $state<number | string>('');
	let selected = $state<string[]>([]);
	let busy = $state(false);
	let confirmOpen = $state(false);
	let touched = $state(false);

	/** Радио/Wi‑Fi slave-интерфейсы — L2 под br*, слушать HTTP на них бессмысленно. */
	function isListenNoise(name: string): boolean {
		const n = name.toLowerCase();
		if (n === 'lo') return true;
		if (/^ra\d/.test(n) || n.startsWith('rai')) return true;
		if (n.startsWith('apcli') || n.startsWith('wlan') || n.startsWith('ath')) return true;
		return false;
	}

	function ifaceRank(name: string): number {
		if (name.startsWith('br')) return 0;
		if (name.startsWith('nwg')) return 1;
		return 2;
	}

	const ifaceOptions = $derived.by((): ChipOption[] => {
		return [...ifaces]
			.filter((i) => !isListenNoise(i.name))
			.sort(
				(a, b) =>
					ifaceRank(a.name) - ifaceRank(b.name) ||
					a.name.localeCompare(b.name, undefined, { numeric: true }),
			)
			.map((i) => ({
				value: i.name,
				label: i.label ? `${i.name} · ${i.label}` : i.name,
			}));
	});

	const port = $derived(Number(portDraft));
	const portValid = $derived(Number.isInteger(port) && port >= 1 && port <= 65535);

	function ifacesEqual(a: string[], b: string[]): boolean {
		if (a.length !== b.length) return false;
		return [...a].sort().join(',') === [...b].sort().join(',');
	}

	const dirty = $derived.by(() => {
		if (!listen) return false;
		if (portValid && port !== listen.port) return true;
		return !ifacesEqual(selected, listen.interfaces);
	});
	const applyDisabled = $derived(busy || !portValid || !dirty || !touched);

	function markTouched(): void {
		touched = true;
	}

	async function load(): Promise<void> {
		listen = await api.serverListenState();
		portDraft = String(listen.port);
		selected = [...listen.interfaces];
		touched = false;
	}

	onMount(async () => {
		// Токен подтверждения из URL-фрагмента — мы только что приехали со
		// старого origin после живой смены адреса. Confirm всегда, даже если
		// UI порта/интерфейсов скрыт уровнем использования.
		const m = window.location.hash.match(/listen-confirm=([0-9a-f]{16,})/);
		if (!m) return;
		history.replaceState(null, '', window.location.pathname + window.location.search);
		try {
			await api.serverListenConfirm(m[1]);
			notifications.success('Новый адрес HTTP-сервера подтверждён и сохранён');
		} catch (e) {
			notifications.error(
				`Не удалось подтвердить смену адреса — бэкенд откатится на старый: ${e instanceof Error ? e.message : String(e)}`,
			);
		}
	});

	$effect(() => {
		let cancelled = false;
		void (async () => {
			try {
				const state = await api.serverListenState();
				if (cancelled) return;
				listen = state;
				portDraft = String(state.port);
				selected = [...state.interfaces];
				touched = false;
			} catch {
				if (!cancelled) listen = null;
				return;
			}
			try {
				const list = await api.getAllInterfaces();
				if (!cancelled) ifaces = list;
			} catch {
				if (!cancelled) ifaces = [];
			}
		})();
		return () => {
			cancelled = true;
		};
	});

	// Браузер на том же origin, что и HTTP-листенер демона? Если нет
	// (yarn dev :5173 → proxy на роутер :2222, KeenDNS :443 → :2222) —
	// hard-redirect на hostname:listenPort уводит в никуда; confirm делаем
	// через текущий /api и остаёмся на странице.
	function isFrontendBehindProxy(daemonPort: number): boolean {
		const pagePort =
			window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
		return String(pagePort) !== String(daemonPort);
	}

	// Куда редиректить после живого перебинда: порт-only смена — тот же хост;
	// смена интерфейсов — первый не-loopback адрес из фактически забинженных
	// (0.0.0.0 покрывает текущий хост — тоже оставляем его).
	function redirectTarget(newPort: number, boundAddrs: string[]): string {
		const cur = window.location;
		const sameIfaces = listen ? ifacesEqual(selected, listen.interfaces) : true;
		let host = cur.hostname;
		if (!sameIfaces && selected.length > 0) {
			const ext = boundAddrs.find((a) => !a.startsWith('127.') && !a.startsWith('0.0.0.0'));
			if (ext) host = ext.slice(0, ext.lastIndexOf(':'));
		}
		return `${cur.protocol}//${host}:${newPort}`;
	}

	async function apply(): Promise<void> {
		if (applyDisabled || !listen) return;
		busy = true;
		const prevPort = listen.port;
		try {
			const res = await api.serverListenChange(port, selected);
			if (isFrontendBehindProxy(prevPort)) {
				await api.serverListenConfirm(res.confirmToken);
				await load();
				notifications.success('Адрес HTTP-сервера подтверждён и сохранён');
				if (port !== prevPort) {
					notifications.warning(
						'Порт демона изменился — обновите VITE_API_TARGET (или reverse-proxy) и перезапустите фронт',
					);
				}
				busy = false;
				return;
			}
			const target = redirectTarget(port, res.boundAddrs);
			notifications.success('Адрес применён — переходим на новый и подтверждаем…');
			window.location.href = `${target}/settings#listen-confirm=${res.confirmToken}`;
		} catch (e) {
			notifications.error(`Не удалось сменить адрес: ${e instanceof Error ? e.message : String(e)}`);
			busy = false;
		}
	}

	const confirmIfaceLabel = $derived(
		selected.length === 0 ? 'все интерфейсы' : selected.join(', '),
	);
</script>

{#if listen}
	{#if listen.pendingConfirm}
		<div class="pending">
			Предыдущая смена адреса ожидает подтверждения (откат в {new Date(listen.confirmDeadline ?? '').toLocaleTimeString()}).
		</div>
	{/if}

	<div class="setting-row hs-port-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Порт</span>
			<span class="setting-description">
				Порт HTTP-сервера панели. Применяется без перезапуска.
			</span>
		</div>
		<div class="hs-port-form">
			<input
				id="hs-port"
				type="number"
				min="1"
				max="65535"
				bind:value={portDraft}
				oninput={markTouched}
				aria-label="Порт HTTP-сервера"
			/>
			{#if portDraft !== '' && !portValid}
				<span class="err">1–65535</span>
			{:else if portValid && port < 1024}
				<span class="warn-inline" title="Порты &lt;1024 могут конфликтовать с системными сервисами">&lt;1024</span>
			{/if}
		</div>
	</div>

	<div class="setting-row hs-iface-row">
		<div class="hs-iface-block">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Интерфейсы</span>
				<span class="setting-description">
					Пусто — все (0.0.0.0). Loopback (127.0.0.1) остаётся всегда.
				</span>
			</div>
			<ChipMultiSelect
				values={selected}
				options={ifaceOptions}
				onchange={(next) => {
					selected = next;
					markTouched();
				}}
				placeholder="Все интерфейсы"
				allowOrphans
			/>
		</div>
	</div>

	<p class="hint">
		Сейчас слушает: <code>{listen.boundAddrs.join(', ')}</code>.
		После применения вы будете перенаправлены на новый адрес; если он не откроется — через ~2 минуты вернётся старый.
	</p>

	{#if touched && dirty}
		<div class="actions">
			<Button
				variant="primary"
				size="sm"
				disabled={applyDisabled}
				loading={busy}
				onclick={() => (confirmOpen = true)}
			>
				{busy ? 'Применяем…' : 'Применить'}
			</Button>
		</div>
	{/if}

	<ConfirmModal
		open={confirmOpen}
		title="Сменить адрес HTTP-сервера"
		message={`Веб-интерфейс переедет на порт ${portValid ? port : '?'} (${confirmIfaceLabel}). Вы будете перенаправлены на новый адрес для подтверждения; без подтверждения через ~2 минуты вернётся текущий адрес.`}
		confirmLabel="Применить"
		variant="primary"
		busy={busy}
		onConfirm={() => {
			confirmOpen = false;
			void apply();
		}}
		onClose={() => {
			if (!busy) confirmOpen = false;
		}}
	/>
{:else}
	<p class="hint">Состояние HTTP-сервера недоступно.</p>
{/if}

<style>
	.pending {
		padding: 0.5rem 0.75rem;
		margin-bottom: 0.25rem;
		font-size: 0.8125rem;
		color: var(--color-warning, #d97706);
		background: color-mix(in srgb, var(--color-warning, #d97706) 10%, transparent);
		border-left: 2px solid var(--color-warning, #d97706);
		border-radius: var(--radius-sm, 6px);
	}

	.hs-port-form {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		flex-shrink: 0;
		min-width: 0;
	}

	.hs-port-form input[type='number'] {
		width: 4.75rem;
	}

	.hs-iface-row {
		display: block;
	}

	.hs-iface-block {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		width: 100%;
		min-width: 0;
	}

	.err {
		color: var(--color-error, #e06a5a);
		font-size: 0.8125rem;
	}

	.warn-inline {
		color: var(--color-warning, #d97706);
		font-size: 0.8125rem;
	}

	.hint {
		color: var(--text-muted);
		font-size: 0.8125rem;
		line-height: 1.45;
		margin: 0.5rem 0 0;
	}

	.hint code {
		font-family: var(--font-mono);
		color: var(--text-secondary);
	}

	.actions {
		display: flex;
		justify-content: flex-end;
		margin-top: 0.75rem;
	}

	@media (max-width: 640px) {
		.hs-port-row {
			display: grid;
			grid-template-columns: minmax(0, 1fr);
			align-items: stretch;
			gap: 0.5rem;
		}

		.hs-port-form {
			justify-content: flex-start;
		}
	}
</style>
