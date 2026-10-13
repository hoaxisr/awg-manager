<script lang="ts">
	import { m } from '$lib/i18n';
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
		catch { configYaml = `# ${m.mihomo_config_not_created_yet()}\n${settingsPreview}`; }
		finally { configLoading = false; }
	}

	function switchMode(value: string) {
		mode = value as typeof mode;
		if (mode === 'expert') void loadConfig();
	}

	async function copyConfig() {
		try { await navigator.clipboard.writeText(configYaml); notifications.success(m.mihomo_config_yaml_copied()); }
		catch { notifications.error(m.mihomo_config_yaml_copy_failed()); }
	}

	async function save() {
		if (saving) return;
		saving = true;
		try {
			let current = get(settings);
			if (!current) { await singboxRouter.reloadSettings(); current = get(settings); }
			if (!current) throw new Error(m.mihomo_config_settings_not_loaded());
			if ((current.routingMode ?? 'tproxy') !== capture) {
				await api.singboxRouterSwitchMode(capture);
				await singboxRouter.reloadSettings();
				current = get(settings) ?? current;
			}
			await api.singboxRouterPutSettings({ ...current, routingEngine: 'mihomo', snifferEnabled: sniffer, mihomoMixedPort: mixedPort, mihomoHttpPort: httpPort, mihomoSocksPort: socksPort });
			await api.mihomoReload();
			await singboxRouter.loadAll();
			if (mode === 'expert') await loadConfig();
			notifications.success(m.mihomo_config_applied());
		} catch (error) { notifications.error(error instanceof Error ? error.message : String(error)); }
		finally { saving = false; }
	}
</script>

<section class="configurator">
	<div class="config-head">
		<div><h3>{m.mihomo_config_title()}</h3><p>{m.mihomo_config_subtitle()}</p></div>
		<SegmentedControl value={mode} options={[{ value: 'simple', label: m.mihomo_config_mode_simple() }, { value: 'expert', label: 'YAML' }]} onchange={switchMode} ariaLabel={m.mihomo_config_mode_aria()} />
	</div>

	{#if mode === 'simple'}
		<div class="flow">
			<Card padding="md"><div class="step"><div class="icon"><Shield size={18} /></div><div><strong>{m.mihomo_config_step_capture_title()}</strong><span>{m.mihomo_config_step_capture_sub()}</span></div><Check size={16} /></div></Card>
			<div class="arrow">→</div>
			<Card padding="md"><div class="step"><div class="icon"><SlidersHorizontal size={18} /></div><div><strong>{m.mihomo_config_step_rules_title()}</strong><span>{m.mihomo_config_step_rules_sub()}</span></div><Check size={16} /></div></Card>
			<div class="arrow">→</div>
			<Card padding="md"><div class="step"><div class="icon"><Network size={18} /></div><div><strong>{m.mihomo_config_step_groups_title()}</strong><span>{m.mihomo_config_step_groups_sub()}</span></div><Check size={16} /></div></Card>
		</div>

		<div class="settings-grid">
			<Card padding="lg"><div class="stack"><h4>{m.mihomo_config_capture_card_title()}</h4><p>{m.mihomo_config_capture_card_desc()}</p><label><span>{m.mihomo_config_capture_mode_lbl()}</span><select bind:value={capture}><option value="tproxy">{m.mihomo_config_capture_opt_tproxy()}</option><option value="fakeip-tun">{m.mihomo_config_capture_opt_fakeip()}</option><option value="policy-tun">{m.mihomo_config_capture_opt_policy()}</option></select></label><label class="check"><input type="checkbox" bind:checked={sniffer} /> {m.mihomo_config_sniffer_lbl()}</label></div></Card>
			<Card padding="lg"><div class="stack"><h4>{m.mihomo_config_ports_card_title()}</h4><p>{m.mihomo_config_ports_card_desc()}</p><div class="ports"><label><span>Mixed</span><input type="number" min="0" max="65535" bind:value={mixedPort} /></label><label><span>HTTP</span><input type="number" min="0" max="65535" bind:value={httpPort} /></label><label><span>SOCKS5</span><input type="number" min="0" max="65535" bind:value={socksPort} /></label></div></div></Card>
		</div>
	{:else}
		<Card padding="lg"><div class="code-head"><div><Code2 size={18} /><strong>{m.mihomo_config_generated_yaml_title()}</strong></div><div class="code-actions"><button onclick={loadConfig} aria-label={m.mihomo_config_refresh_yaml_aria()} title={m.mihomo_config_refresh_btn()}><span class:spin={configLoading}><RefreshCw size={14} /></span></button><button onclick={copyConfig} aria-label={m.mihomo_config_copy_yaml_aria()} title={m.mihomo_config_copy_btn()}><Clipboard size={14} /></button></div></div><pre>{configYaml || settingsPreview}</pre><p class="expert-hint">{m.mihomo_config_expert_hint()}</p></Card>
	{/if}

	<div class="save-row"><Button variant="primary" size="md" onclick={save} loading={saving}><Save size={16} />{m.mihomo_config_apply_btn()}</Button></div>
</section>

<style>
	.configurator{display:grid;gap:14px}.config-head{display:flex;align-items:flex-start;justify-content:space-between;gap:14px}.config-head h3,.config-head p,h4{margin:0}.config-head p,.stack p,.expert-hint{margin-top:4px;color:var(--text-muted);font-size:12px;line-height:1.5}.flow{display:grid;grid-template-columns:minmax(0,1fr) auto minmax(0,1fr) auto minmax(0,1fr);align-items:center;gap:8px}.step{display:grid;grid-template-columns:auto minmax(0,1fr) auto;align-items:center;gap:10px}.step>div:not(.icon){display:grid}.step strong{font-size:13px}.step span{color:var(--text-muted);font-size:11px}.icon{display:grid;place-items:center;width:34px;height:34px;border-radius:9px;background:var(--accent-soft);color:var(--accent)}.arrow{color:var(--text-muted)}.settings-grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.stack{display:grid;gap:12px}.stack label{display:grid;gap:5px;font-size:12px;color:var(--text-secondary)}select,input{width:100%;box-sizing:border-box;padding:8px 10px;border:1px solid var(--border);border-radius:7px;background:var(--bg-primary);color:var(--text-primary);font:inherit}.check{display:flex!important;grid-template-columns:auto 1fr!important;align-items:center}.check input{width:auto}.ports{display:grid;grid-template-columns:repeat(3,1fr);gap:8px}.save-row{display:flex;justify-content:flex-end}.code-head{display:flex;align-items:center;justify-content:space-between;gap:10px;margin-bottom:12px}.code-head>div{display:flex;align-items:center;gap:8px}.code-actions button{display:grid;place-items:center;width:30px;height:30px;border:0;border-radius:7px;background:transparent;color:var(--text-muted);cursor:pointer}.code-actions button:hover{background:var(--bg-tertiary);color:var(--text-primary)}pre{max-height:520px;overflow:auto;margin:0;padding:14px;border:1px solid var(--border);border-radius:8px;background:var(--bg-primary);color:var(--text-primary);font:12px/1.55 var(--font-mono)}.spin{animation:spin .8s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(max-width:850px){.flow{grid-template-columns:1fr}.arrow{transform:rotate(90deg);justify-self:center}.settings-grid{grid-template-columns:1fr}}@media(max-width:520px){.config-head{flex-direction:column}.config-head :global(.segmented-control){width:100%}.ports{grid-template-columns:1fr}.save-row :global(button){width:100%}}
</style>
