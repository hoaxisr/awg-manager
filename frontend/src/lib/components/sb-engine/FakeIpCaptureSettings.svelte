<script lang="ts">
	// Настройки режима FakeIP внутри карточки «Захват трафика»: пулы v4/v6,
	// TCP/IP-стек, MTU, «DNS для клиентов» и активный tun-интерфейс.
	//
	// ПОЧЕМУ НЕ `EngineSettingsCard`. Та карточка (вкладка FakeIP) кроме этих
	// полей несла тумблер движка, «Перезапустить», WAN-интерфейс и sniffer.
	// Тумблер и рестарт теперь в шапке страницы, а WAN и sniffer — общие для всех
	// трёх режимов настройки и уезжают в карточку «Здоровье»: показывать их ещё и
	// здесь означало бы два места правки одного поля. Плюс сама
	// `EngineSettingsCard` уже осиротела — вкладки FakeIP не существует.
	//
	// «DNS для клиентов» и имя tun-интерфейса — read-only из статуса и парны:
	// адрес нужно прописать на клиентах руками, а по имени интерфейса —
	// назначить выход в политике доступа NDMS. Без второй половины первая
	// бесполезна (пробел §3 спеки, п. 7 инвентаря задачи 1).
	import { Dropdown, Input, type DropdownOption } from '$lib/components/ui';
	import { TriangleAlert } from 'lucide-svelte';
	import { notifications } from '$lib/stores/notifications';
	import { mtuError, poolError } from './fakeipFields';
	import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

	interface Props {
		cfg: SingboxRouterSettings;
		status: SingboxRouterStatus | null;
		onPatch: (patch: Partial<SingboxRouterSettings>) => void | Promise<void>;
	}
	let { cfg, status, onPatch }: Props = $props();

	let saving = $state(false);

	// Драфты текстовых полей: правим локально, коммитим на change/blur. Пока идёт
	// сохранение, эхо пропов не затирает то, что пользователь печатает.
	const pool4 = $state({ v: '' });
	const pool6 = $state({ v: '' });
	const mtu = $state({ v: '' });
	$effect(() => {
		if (!saving) pool4.v = cfg.fakeipPool4 ?? '';
	});
	$effect(() => {
		if (!saving) pool6.v = cfg.fakeipPool6 ?? '';
	});
	$effect(() => {
		if (!saving) mtu.v = cfg.fakeipMtu != null ? String(cfg.fakeipMtu) : '';
	});

	const stackOptions: DropdownOption<'gvisor' | 'system'>[] = [
		{ value: 'gvisor', label: 'gvisor' },
		{ value: 'system', label: 'system' },
	];

	const fakeipDns = $derived(status?.fakeipDns ?? '');
	const fakeipIface = $derived(status?.fakeipIface ?? '');

	async function save(patch: Partial<SingboxRouterSettings>): Promise<void> {
		if (saving) return;
		saving = true;
		try {
			await onPatch(patch);
		} finally {
			saving = false;
		}
	}

	function commitPool(family: 4 | 6): void {
		const draft = family === 4 ? pool4 : pool6;
		const current = (family === 4 ? cfg.fakeipPool4 : cfg.fakeipPool6) ?? '';
		const v = draft.v.trim();
		if (v === current) return;
		const err = poolError(v, family);
		if (err !== null) {
			notifications.error(err);
			draft.v = current;
			return;
		}
		void save(family === 4 ? { fakeipPool4: v } : { fakeipPool6: v });
	}

	function commitMtu(): void {
		const err = mtuError(mtu.v);
		if (err !== null) {
			notifications.error(err);
			mtu.v = cfg.fakeipMtu != null ? String(cfg.fakeipMtu) : '';
			return;
		}
		const n = Number(mtu.v.trim());
		if (n === cfg.fakeipMtu) return;
		void save({ fakeipMtu: n });
	}
</script>

<section class="sec">
	<div class="sec-cap">Пулы и туннель</div>

	<div class="field">
		<span class="lbl">fakeip-пул v4</span>
		<Input
			value={pool4.v}
			placeholder="10.128.0.0/10"
			disabled={saving}
			fullWidth
			oninput={(v) => (pool4.v = v)}
			onchange={() => commitPool(4)}
		/>
	</div>

	<div class="field">
		<span class="lbl">fakeip-пул v6</span>
		<Input
			value={pool6.v}
			placeholder="3f80::/10 (пусто — выкл)"
			disabled={saving}
			fullWidth
			oninput={(v) => (pool6.v = v)}
			onchange={() => commitPool(6)}
		/>
	</div>

	<div class="field">
		<span class="lbl">TCP/IP-стек</span>
		<Dropdown
			value={cfg.fakeipStack ?? 'gvisor'}
			options={stackOptions}
			disabled={saving}
			fullWidth
			onchange={(v) => {
				if (v !== (cfg.fakeipStack ?? 'gvisor')) void save({ fakeipStack: v });
			}}
		/>
		{#if (cfg.fakeipStack ?? 'gvisor') === 'system'}
			<p class="hint">Ниже потолок скорости, бэкенд форсит gso:false.</p>
		{/if}
	</div>

	<div class="field">
		<span class="lbl">MTU tun</span>
		<Input
			type="number"
			value={mtu.v}
			placeholder="1500"
			disabled={saving}
			fullWidth
			oninput={(v) => (mtu.v = v)}
			onchange={commitMtu}
		/>
	</div>

	<p class="hint">Пул, стек и MTU применяются с перезапуском sing-box.</p>
</section>

{#if fakeipDns || fakeipIface}
	<section class="sec">
		<div class="sec-cap">Подключение клиентов</div>

		{#if fakeipDns}
			<div class="stat-line">
				<span class="stat-label">DNS для клиентов</span>
				<span class="stat-value">{fakeipDns}</span>
			</div>
			<p class="hint hint-warning">
				<TriangleAlert size={13} />
				Пропишите этот DNS на клиентах вручную — авто-fallback при падении прокси нет.
			</p>
		{/if}

		{#if fakeipIface}
			<div class="stat-line">
				<span class="stat-label">tun-интерфейс</span>
				<span class="stat-value">{fakeipIface}</span>
			</div>
			<p class="hint">
				Чтобы завернуть через fakeip весь трафик устройства, назначьте ему выход
				{fakeipIface} в <a href="/router/policies">политике доступа</a>.
			</p>
		{/if}
	</section>
{/if}

<style>
	/* Секции повторяют идиому соседей по карточке (TrafficSourceSettings,
	   PolicyTunCard): scoped-стили родителя в дочерний компонент не проникают,
	   поэтому у каждого свои. */
	.sec {
		padding: 14px var(--sp-4);
		border-bottom: 1px solid var(--border);
		display: flex;
		flex-direction: column;
		gap: 10px;
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
	/* Обычная подсказка — поток текста, а НЕ flex: во flex-контейнере ссылка и
	   точка после неё становятся отдельными элементами и разъезжаются на gap
	   («политике доступа .»). Flex нужен только строке с иконкой. */
	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}
	.hint-warning {
		display: flex;
		align-items: flex-start;
		gap: 5px;
		color: var(--color-warning, #dab856);
	}
	.hint :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
	}
	.hint a {
		color: var(--accent);
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
		word-break: break-all;
	}
</style>
