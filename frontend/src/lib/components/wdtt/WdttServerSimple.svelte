<script lang="ts">
	import { onMount } from 'svelte';
	import { Button, Input, Toggle, SegmentedControl, ChipMultiSelect } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { createIngressMutationLock } from '$lib/utils/ingressMutation';
	import type { NatMode } from '$lib/utils/network';
	import StepPill from '$lib/components/sb-router/StepPill.svelte';
	import WizardStep from '$lib/components/sb-router/WizardStep.svelte';
	import ProcessLogBox from '$lib/components/freeturn/ProcessLogBox.svelte';
	import { ServerAccessPolicyDropdown } from '$lib/components/servers';
	import type { LogInstanceItem } from '$lib/components/freeturn/LogInstanceSwitcher.svelte';
	import type { WdttProcessStatus, WdttServerConfig } from '$lib/types';

	const withIngressLock = createIngressMutationLock();
	const wdttIface = 'wdtt0';

	const natModeOptions: { value: NatMode; label: string }[] = [
		{ value: 'full', label: 'Полный' },
		{ value: 'internet-only', label: 'Интернет' },
		{ value: 'none', label: 'Без NAT' }
	];

	interface Props {
		server: WdttServerConfig;
		running?: boolean;
		saving?: boolean;
		generating?: boolean;
		status?: WdttProcessStatus;
		serverInstanceId?: string;
		generatedLink?: string;
		generatedLinkQwdtt?: string;
		genPeer?: string;
		genVKHashes?: string;
		onSave: (cfg: WdttServerConfig) => void | Promise<void>;
		onToggle: (on: boolean) => void | Promise<void>;
		onGenerate: (peer: string, vkHashes: string[]) => Promise<{ link: string; linkQwdtt?: string; peer: string } | null>;
		instances?: LogInstanceItem[];
		selectedInstanceId?: string;
		onSelectInstance?: (id: string) => void;
	}

	let {
		server,
		running = false,
		saving = false,
		generating = false,
		status,
		serverInstanceId = 'default',
		generatedLink = '',
		generatedLinkQwdtt = '',
		genPeer = $bindable(''),
		genVKHashes = $bindable(''),
		onSave,
		onToggle,
		onGenerate,
		instances = [],
		selectedInstanceId = '',
		onSelectInstance
	}: Props = $props();

	let starting = $state(false);
	let loadingWanPeer = $state(false);
	let showPassword = $state(false);
	let togglingIngress = $state(false);
	let lanSegmentOptions = $state<{ value: string; label: string }[]>([]);

	let natMode = $derived((server.natMode || 'full') as NatMode);

	onMount(async () => {
		try {
			const segs = await api.listManagedLANSegments();
			lanSegmentOptions = segs.map((s) => ({ value: s.name, label: s.label || s.name }));
		} catch {
			lanSegmentOptions = [];
		}
		try {
			const s = await api.singboxRouterGetSettings();
			const refs = s.ingressInterfaces ?? [];
			server.ingressEnabled = refs.includes(`iface:${wdttIface}`);
		} catch {
			/* ignore */
		}
	});

	async function handleSetNATMode(mode: NatMode) {
		server.natMode = mode;
		await onSave(server);
	}

	async function handlePolicyChange(policy: string) {
		server.policy = policy;
		await onSave(server);
	}

	async function handleSetLANSegments(segments: string[]) {
		server.lanSegments = segments;
		await onSave(server);
	}

	async function handleToggleIngress(enabled: boolean) {
		togglingIngress = true;
		try {
			await withIngressLock(async () => {
				const s = await api.singboxRouterGetSettings();
				const set = new Set(s.ingressInterfaces ?? []);
				const ref = `iface:${wdttIface}`;
				if (enabled) set.add(ref);
				else set.delete(ref);
				const next = [...set];
				await api.singboxRouterPutSettings({ ...s, ingressInterfaces: next });
				server.ingressEnabled = enabled;
			});
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось изменить sing-box ingress');
		} finally {
			togglingIngress = false;
		}
	}

	const listenPort = $derived.by(() => {
		const listen = server.listen?.trim() ?? '';
		if (!listen) return '56000';
		const idx = listen.lastIndexOf(':');
		return idx >= 0 ? listen.slice(idx + 1) : listen;
	});

	const step1Done = $derived(!!server.password.trim());
	const step2Done = $derived(step1Done && !!server.listen.trim());
	const step3Done = $derived(step2Done);
	const canSave = $derived(step1Done && !saving && !starting);
	const canStart = $derived(step3Done && !saving && !starting);

	function randomPassword() {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		server.password = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
	}

	async function fillWanPeer() {
		loadingWanPeer = true;
		try {
			const ip = await api.getWANIP();
			genPeer = ip.includes(':') ? ip : `${ip}:${listenPort}`;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось определить WAN IP');
		} finally {
			loadingWanPeer = false;
		}
	}

	async function saveOnly() {
		if (!canSave) return;
		await onSave(server);
	}

	async function startOnly() {
		if (!canStart || running) return;
		starting = true;
		try {
			await onSave(server);
			await onToggle(true);
		} finally {
			starting = false;
		}
	}

	async function generateLinkNow() {
		let peerParam = genPeer.trim();
		if (peerParam && !peerParam.includes(':')) {
			peerParam = `${peerParam}:${listenPort}`;
		}
		const hashes = genVKHashes
			.split(/[,;\s]+/)
			.map((h) => h.trim())
			.filter(Boolean);
		const result = await onGenerate(peerParam, hashes);
		if (result?.link) {
			notifications.success('Ссылка wdtt:// сгенерирована');
		}
	}

	async function saveStartAndLink() {
		if (!canStart) return;
		starting = true;
		try {
			await onSave(server);
			if (!running) await onToggle(true);
			await generateLinkNow();
		} finally {
			starting = false;
		}
	}

	async function copyLink() {
		if (!generatedLink) return;
		const ok = await copyToClipboard(generatedLink);
		if (ok) notifications.success('Ссылка скопирована');
	}
</script>

<div class="wdtt-server-wrap">
	<p class="wdtt-server-lead">
		WDTT-сервер на роутере: DTLS на WAN, внутренний WireGuard (<code>{wdttIface}</code>) для клиентов.
		NAT и доступ в LAN настраиваются здесь (интерфейс Entware, NDMS его не видит); встроенный NAT
		wdtt-server отключён (<code>-no-nat</code>).
	</p>

	<div class="wdtt-server-steps">
		<StepPill n={1} label="Пароль" active={true} done={step1Done} />
		<StepPill n={2} label="Порты" active={step1Done} done={step2Done} />
		<StepPill n={3} label="Доступ" active={step2Done} done={step3Done} />
		<StepPill n={4} label="Запуск" active={step3Done} done={running} />
		<StepPill n={5} label="Ссылка" active={running} done={!!generatedLink} />
	</div>

	<WizardStep n={1} title="Пароль (-password)" hint="общий ключ для всех клиентов" active={true}>
		<div class="wdtt-row">
			<Input
				type={showPassword ? 'text' : 'password'}
				bind:value={server.password}
				placeholder="секретный пароль"
			/>
			<Button variant="secondary" onclick={() => (showPassword = !showPassword)}>
				{showPassword ? 'Скрыть' : 'Показать'}
			</Button>
			<Button variant="secondary" onclick={randomPassword}>Сгенерировать</Button>
		</div>
	</WizardStep>

	<WizardStep n={2} title="Порты" hint="DTLS на WAN и внутренний WG-порт" active={step1Done}>
		<p class="wdtt-readonly">
			DTLS (-listen): <code>{server.listen || '0.0.0.0:56000'}</code>
		</p>
		<p class="wdtt-readonly">
			WG (-wg-port): <code>{server.wgPort || 56001}</code>
		</p>
		<p class="wdtt-hint">
			Если FreeTurn-сервер уже слушает 56000, создайте второй инстанс WDTT — порт подберётся
			автоматически (56001, 56002…).
		</p>
	</WizardStep>

	<WizardStep
		n={3}
		title="Настройки доступа ({wdttIface})"
		hint="NAT, LAN и sing-box"
		active={step2Done}
	>
		<div class="server-detail-card wdtt-access-settings" class:inactive={!step2Done}>
			<div class="setting-row">
				<div class="setting-copy">
					<span class="setting-title">NAT</span>
					{#if natMode === 'full'}
						<span class="setting-description">
							Клиент выходит в интернет через iptables/NAT роутера; в LAN виден как роутер.
						</span>
					{:else if natMode === 'internet-only'}
						<span class="setting-description">
							Интернет через NAT роутера; в LAN виден с VPN-адресом 10.66.66.x.
						</span>
					{:else}
						<span class="setting-description">
							Без NAT — только маршрутизация awg-manager / политики доступа.
						</span>
					{/if}
					{#if server.ingressEnabled && natMode === 'full'}
						<span class="setting-description setting-description-warning">
							Интернет через sing-box — NAT влияет только на видимость в LAN.
						</span>
					{/if}
				</div>
				<div class="setting-control">
					<SegmentedControl
						value={natMode}
						options={natModeOptions}
						ariaLabel="Режим NAT"
						disabled={saving || !step2Done}
						onchange={handleSetNATMode}
					/>
				</div>
			</div>

			<div class="setting-row">
				<div class="setting-copy">
					<span class="setting-title">Доступ в LAN</span>
					<span class="setting-description">Сегменты LAN для клиентов qWDTT.</span>
				</div>
				<div class="setting-control">
					<ChipMultiSelect
						values={server.lanSegments ?? []}
						options={lanSegmentOptions}
						onchange={handleSetLANSegments}
						disabled={saving || !step2Done}
					/>
				</div>
			</div>

			<div class="setting-row setting-row-toggle">
				<div class="setting-copy">
					<span class="setting-title">Маршрутизация через sing-box</span>
					<span class="setting-description">Заворачивать интернет-трафик клиентов {wdttIface} в sing-box.</span>
				</div>
				<div class="setting-control setting-control-toggle">
					<Toggle
						checked={!!server.ingressEnabled}
						onchange={handleToggleIngress}
						disabled={togglingIngress || !step2Done}
						spinner="before"
					/>
				</div>
			</div>

			<ServerAccessPolicyDropdown
				policy={server.policy ?? 'none'}
				disabled={saving || !step2Done}
				onchange={handlePolicyChange}
			/>
		</div>
	</WizardStep>

	<WizardStep n={4} title="Запуск сервера" hint="сохранить и поднять wdtt-server" active={step3Done}>
		<div class="wdtt-actions">
			<Button disabled={!canSave || saving} onclick={saveOnly}>Сохранить</Button>
			{#if running}
				<Button variant="danger" disabled={starting} onclick={() => onToggle(false)}>Остановить</Button>
			{:else}
				<Button variant="primary" disabled={!canStart || starting} onclick={startOnly}>Запустить</Button>
			{/if}
		</div>
		<ProcessLogBox
			log={status?.log}
			bind:debug={server.debug}
			showDebugToggle
			{instances}
			{selectedInstanceId}
			{onSelectInstance}
		/>
	</WizardStep>

	<WizardStep n={5} title="Ссылка для клиента" hint="peer = WAN IP : DTLS-порт" active={running || step3Done}>
		<div class="wdtt-row">
			<Input bind:value={genPeer} placeholder="203.0.113.1:56000 или пусто → авто WAN" />
			<Button variant="secondary" disabled={loadingWanPeer} onclick={fillWanPeer}>WAN IP</Button>
		</div>
		<Input
			bind:value={genVKHashes}
			placeholder="VK-хеши через запятую (необязательно для ссылки)"
		/>
		<div class="wdtt-actions">
			<Button disabled={!canSave || generating} onclick={generateLinkNow}>Сгенерировать ссылку</Button>
			<Button variant="primary" disabled={!canStart || generating || starting} onclick={saveStartAndLink}>
				Сохранить · запустить · ссылка
			</Button>
		</div>
		{#if generatedLink}
			<div class="wdtt-link-box">
				<p class="wdtt-link-label">wdtt:// (colon)</p>
				<code>{generatedLink}</code>
				<Button variant="secondary" onclick={copyLink}>Копировать wdtt://</Button>
			</div>
		{/if}
		{#if generatedLinkQwdtt}
			<div class="wdtt-link-box">
				<p class="wdtt-link-label">qwdtt:// (для qWDTT / Android)</p>
				<code>{generatedLinkQwdtt}</code>
				<Button
					variant="secondary"
					onclick={async () => {
						if (await copyToClipboard(generatedLinkQwdtt)) notifications.success('qwdtt:// скопирована');
					}}>Копировать qwdtt://</Button
				>
			</div>
		{/if}
	</WizardStep>
</div>

<style>
	.wdtt-server-wrap {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.wdtt-server-lead {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	.wdtt-server-steps {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.wdtt-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}

	.wdtt-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin: 0.75rem 0;
	}

	.wdtt-readonly {
		margin: 0.25rem 0;
		font-size: 0.875rem;
	}

	.wdtt-hint {
		margin: 0.5rem 0 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.wdtt-link-box {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-top: 0.75rem;
		padding: 0.75rem;
		background: var(--color-surface-secondary, rgba(0, 0, 0, 0.04));
		border-radius: 6px;
		word-break: break-all;
	}

	.wdtt-link-label {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.wdtt-link-box code {
		font-size: 0.75rem;
	}

	.wdtt-access-settings {
		gap: 0;
	}

	.wdtt-access-settings.inactive {
		opacity: 0.55;
		pointer-events: none;
	}
</style>
