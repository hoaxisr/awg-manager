<script lang="ts">
	import { get } from 'svelte/store';
	import { Check, Clipboard, Code2, Network, RefreshCw, Save, Shield, SlidersHorizontal } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card, SegmentedControl } from '$lib/components/ui';

	const settings = singboxRouter.settings;
	let mode = $state<'simple' | 'expert'>('simple');
	let capture = $state<'tproxy' | 'fakeip-tun' | 'policy-tun'>('tproxy');
	let sniffer = $state(true);
	let mixedPort = $state(0);
	let httpPort = $state(0);
	let socksPort = $state(0);
	let initialized = $state(false);
	let saving = $state(false);
	let configLoading = $state(false);
	let configYaml = $state('');

	$effect(() => {
		const value = $settings;
		if (!value || initialized) return;
		capture = value.routingMode ?? 'tproxy';
		sniffer = value.snifferEnabled;
		mixedPort = value.mihomoMixedPort ?? 0;
		httpPort = value.mihomoHttpPort ?? 0;
		socksPort = value.mihomoSocksPort ?? 0;
		initialized = true;
	});

	const settingsPreview = $derived([
		'mode: rule',
		'external-controller: 127.0.0.1:9090',
		capture === 'tproxy' ? 'tproxy-port: 51271' : 'tun:\n  enable: true\n  stack: system',
		mixedPort ? `mixed-port: ${mixedPort}` : '',
		httpPort ? `port: ${httpPort}` : '',
		socksPort ? `socks-port: ${socksPort}` : '',
		`sniffer:\n  enable: ${sniffer}`,
		'dns:\n  enable: true\n  listen: 127.0.0.1:1053',
	].filter(Boolean).join('\n'));

	async function loadConfig() {
		configLoading = true;
		try { configYaml = (await api.mihomoConfig()).yaml; }
		catch { configYaml = `# config.yaml ещё не создан\n${settingsPreview}`; }
		finally { configLoading = false; }
	}

	function switchMode(value: string) {
		mode = value as typeof mode;
		if (mode === 'expert') void loadConfig();
	}

	async function copyConfig() {
		try { await navigator.clipboard.writeText(configYaml); notifications.success('YAML скопирован'); }
		catch { notifications.error('Не удалось скопировать YAML'); }
	}

	async function save() {
		if (saving) return;
		saving = true;
		try {
			let current = get(settings);
			if (!current) { await singboxRouter.reloadSettings(); current = get(settings); }
			if (!current) throw new Error('Настройки маршрутизации не загружены');
			if ((current.routingMode ?? 'tproxy') !== capture) {
				await api.singboxRouterSwitchMode(capture);
				await singboxRouter.reloadSettings();
				current = get(settings) ?? current;
			}
			await api.singboxRouterPutSettings({ ...current, routingEngine: 'mihomo', snifferEnabled: sniffer, mihomoMixedPort: mixedPort, mihomoHttpPort: httpPort, mihomoSocksPort: socksPort });
			await api.mihomoReload();
			await singboxRouter.loadAll();
			if (mode === 'expert') await loadConfig();
			notifications.success('Конфигурация Mihomo применена');
		} catch (error) { notifications.error(error instanceof Error ? error.message : String(error)); }
		finally { saving = false; }
	}
</script>

<section class="configurator">
	<div class="config-head">
		<div><h3>Генератор конфигурации</h3><p>Основные настройки собираются в безопасный YAML и применяются через AWG Manager.</p></div>
		<SegmentedControl value={mode} options={[{ value: 'simple', label: 'Простой' }, { value: 'expert', label: 'YAML' }]} onchange={switchMode} ariaLabel="Режим конфигуратора" />
	</div>

	{#if mode === 'simple'}
		<div class="flow">
			<Card padding="md"><div class="step"><div class="icon"><Shield size={18} /></div><div><strong>Перехват трафика</strong><span>Прозрачная маршрутизация клиентов роутера</span></div><Check size={16} /></div></Card>
			<div class="arrow">→</div>
			<Card padding="md"><div class="step"><div class="icon"><SlidersHorizontal size={18} /></div><div><strong>Правила и DNS</strong><span>Общая модель маршрутизации AWG Manager</span></div><Check size={16} /></div></Card>
			<div class="arrow">→</div>
			<Card padding="md"><div class="step"><div class="icon"><Network size={18} /></div><div><strong>Группы Mihomo</strong><span>Выбор узлов без перезапуска</span></div><Check size={16} /></div></Card>
		</div>

		<div class="settings-grid">
			<Card padding="lg"><div class="stack"><h4>Прозрачный перехват</h4><p>TProxy и TUN направляют трафик устройств в движок. Локальные proxy-порты ниже работают независимо.</p><label><span>Режим</span><select bind:value={capture}><option value="tproxy">TProxy — TCP и UDP через netfilter</option><option value="fakeip-tun">TUN + FakeIP</option><option value="policy-tun">Политики доступа + TUN</option></select></label><label class="check"><input type="checkbox" bind:checked={sniffer} /> Анализировать HTTP/TLS/QUIC назначения</label></div></Card>
			<Card padding="lg"><div class="stack"><h4>Локальные proxy-порты</h4><p>Mixed, HTTP и SOCKS можно включать одновременно. Значение 0 отключает порт.</p><div class="ports"><label><span>Mixed</span><input type="number" min="0" max="65535" bind:value={mixedPort} /></label><label><span>HTTP</span><input type="number" min="0" max="65535" bind:value={httpPort} /></label><label><span>SOCKS5</span><input type="number" min="0" max="65535" bind:value={socksPort} /></label></div></div></Card>
		</div>
	{:else}
		<Card padding="lg"><div class="code-head"><div><Code2 size={18} /><strong>Сгенерированный config.yaml</strong></div><div class="code-actions"><button onclick={loadConfig} aria-label="Обновить YAML" title="Обновить"><span class:spin={configLoading}><RefreshCw size={14} /></span></button><button onclick={copyConfig} aria-label="Скопировать YAML" title="Скопировать"><Clipboard size={14} /></button></div></div><pre>{configYaml || settingsPreview}</pre><p class="expert-hint">Это полный активный YAML: входящие порты, DNS, прокси, providers, группы и правила. Редактируйте сущности через простой режим — генератор проверит конфигурацию перед перезапуском.</p></Card>
	{/if}

	<div class="save-row"><Button variant="primary" size="md" onclick={save} loading={saving}><Save size={16} />Применить конфигурацию</Button></div>
</section>

<style>
	.configurator{display:grid;gap:14px}.config-head{display:flex;align-items:flex-start;justify-content:space-between;gap:14px}.config-head h3,.config-head p,h4{margin:0}.config-head p,.stack p,.expert-hint{margin-top:4px;color:var(--text-muted);font-size:12px;line-height:1.5}.flow{display:grid;grid-template-columns:minmax(0,1fr) auto minmax(0,1fr) auto minmax(0,1fr);align-items:center;gap:8px}.step{display:grid;grid-template-columns:auto minmax(0,1fr) auto;align-items:center;gap:10px}.step>div:not(.icon){display:grid}.step strong{font-size:13px}.step span{color:var(--text-muted);font-size:11px}.icon{display:grid;place-items:center;width:34px;height:34px;border-radius:9px;background:var(--accent-soft);color:var(--accent)}.arrow{color:var(--text-muted)}.settings-grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.stack{display:grid;gap:12px}.stack label{display:grid;gap:5px;font-size:12px;color:var(--text-secondary)}select,input{width:100%;box-sizing:border-box;padding:8px 10px;border:1px solid var(--border);border-radius:7px;background:var(--bg-primary);color:var(--text-primary);font:inherit}.check{display:flex!important;grid-template-columns:auto 1fr!important;align-items:center}.check input{width:auto}.ports{display:grid;grid-template-columns:repeat(3,1fr);gap:8px}.save-row{display:flex;justify-content:flex-end}.code-head{display:flex;align-items:center;justify-content:space-between;gap:10px;margin-bottom:12px}.code-head>div{display:flex;align-items:center;gap:8px}.code-actions button{display:grid;place-items:center;width:30px;height:30px;border:0;border-radius:7px;background:transparent;color:var(--text-muted);cursor:pointer}.code-actions button:hover{background:var(--bg-tertiary);color:var(--text-primary)}pre{max-height:520px;overflow:auto;margin:0;padding:14px;border:1px solid var(--border);border-radius:8px;background:var(--bg-primary);color:var(--text-primary);font:12px/1.55 var(--font-mono)}.spin{animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(max-width:850px){.flow{grid-template-columns:1fr}.arrow{transform:rotate(90deg);justify-self:center}.settings-grid{grid-template-columns:1fr}}@media(max-width:520px){.config-head{flex-direction:column}.config-head :global(.segmented-control){width:100%}.ports{grid-template-columns:1fr}.save-row :global(button){width:100%}}
</style>
