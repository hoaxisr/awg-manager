<script lang="ts">
  import { onMount } from 'svelte';
  import {
    ArrowLeft, Check, Globe, Shield, ShieldOff, Zap, Plus, Layers, Network,
    Tv, Send, MessageSquare, Camera, Sparkles, Film, Music, Gamepad2, ShoppingCart, AlertTriangle
  } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { Button, Card, SegmentedControl } from '$lib/components/ui';
  import type { MihomoNativeGroup, MihomoNativeProxy, MihomoNativeRule, MihomoNativeSubscription, MihomoRuntimeProxy } from '$lib/types';

  interface Props {
    onClose: () => void;
    onSaved: () => void;
    groups: MihomoNativeGroup[];
    proxies: MihomoNativeProxy[];
    subscriptions: MihomoNativeSubscription[];
    runtimeProxies: MihomoRuntimeProxy[];
  }

  let { onClose, onSaved, groups = [], proxies = [], subscriptions = [], runtimeProxies = [] }: Props = $props();

  // Wizard state
  let currentStep = $state<1 | 2 | 3>(1);

  // Step 1: Matcher
  let matcherTab = $state<'catalog' | 'custom'>('catalog');
  let selectedService = $state<{ name: string; type: string; payload: string; icon: any } | null>(null);

  let customType = $state<'GEOSITE' | 'DOMAIN-SUFFIX' | 'DOMAIN' | 'IP-CIDR' | 'RULE-SET'>('GEOSITE');
  let customPayload = $state('');

  // Step 2: Outbound
  let outboundType = $state<'group' | 'proxy' | 'direct' | 'reject'>('group');
  let selectedGroup = $state('');
  let selectedProxy = $state('');

  // New Group inline creation (optional)
  let createNewGroup = $state(false);
  let newGroupName = $state('');
  let newGroupType = $state<'url-test' | 'select' | 'fallback'>('url-test');
  let newGroupMembers = $state<string[]>([]);

  // Step 3: Saving
  let saving = $state(false);

  // Popular Services Catalog with icons and Mihomo GEOSITE tags
  const CATALOG_SERVICES = [
    { name: 'YouTube', type: 'GEOSITE', payload: 'youtube', icon: Tv, desc: 'Видео и стриминг YouTube/Google Video' },
    { name: 'Telegram', type: 'GEOSITE', payload: 'telegram', icon: Send, desc: 'Мессенджер Telegram и звонки' },
    { name: 'Discord', type: 'GEOSITE', payload: 'discord', icon: MessageSquare, desc: 'Голосовые и текстовые чаты Discord' },
    { name: 'Instagram', type: 'GEOSITE', payload: 'instagram', icon: Camera, desc: 'Instagram и сервисы Meta' },
    { name: 'Twitter / X', type: 'GEOSITE', payload: 'twitter', icon: Globe, desc: 'Социальная сеть X (Twitter)' },
    { name: 'OpenAI / ChatGPT', type: 'GEOSITE', payload: 'openai', icon: Sparkles, desc: 'ChatGPT, API и веб-сервисы OpenAI' },
    { name: 'TikTok', type: 'GEOSITE', payload: 'tiktok', icon: Film, desc: 'Сервисы видео TikTok' },
    { name: 'Spotify', type: 'GEOSITE', payload: 'spotify', icon: Music, desc: 'Музыкальный стриминг Spotify' },
    { name: 'Twitch', type: 'GEOSITE', payload: 'twitch', icon: Tv, desc: 'Стриминговая платформа Twitch' },
    { name: 'Steam', type: 'GEOSITE', payload: 'steam', icon: Gamepad2, desc: 'Магазин и сообщество Steam' },
    { name: 'Netflix', type: 'GEOSITE', payload: 'netflix', icon: Film, desc: 'Онлайн-кинотеатр Netflix' },
    { name: 'RuTracker', type: 'DOMAIN-SUFFIX', payload: 'rutracker.org', icon: Globe, desc: 'Трекер rutracker.org и зеркала' },
  ];

  // Available groups
  const availableGroups = $derived.by(() => {
    const list: Array<{ name: string; type: string; label: string }> = [];
    for (const g of groups) {
      if (g.enabled) list.push({ name: g.name, type: g.type, label: g.name });
    }
    for (const s of subscriptions) {
      if (s.enabled && s.groupName && !list.some(item => item.name === s.groupName)) {
        list.push({ name: s.groupName, type: 'url-test', label: s.name });
      }
    }
    return list;
  });

  // Available individual proxies
  const availableProxies = $derived.by(() => {
    const list: Array<{ name: string; label: string }> = [];
    for (const p of proxies) {
      if (p.enabled) list.push({ name: p.name, label: p.name });
    }
    return list;
  });

  $effect(() => {
    if (availableGroups.length > 0 && !selectedGroup) {
      selectedGroup = availableGroups[0].name;
    }
    if (availableProxies.length > 0 && !selectedProxy) {
      selectedProxy = availableProxies[0].name;
    }
  });

  const selectedGroupObj = $derived(availableGroups.find((g) => g.name === selectedGroup));
  const isSelectedGroupLoadBalance = $derived(selectedGroupObj?.type === 'load-balance');

  const ruleSummary = $derived.by(() => {
    const matcher = matcherTab === 'catalog' && selectedService
      ? `${selectedService.name} (${selectedService.type}: ${selectedService.payload})`
      : `${customType}: ${customPayload.trim()}`;

    let target = 'DIRECT (Напрямую)';
    if (outboundType === 'reject') target = 'REJECT (Блокировать)';
    else if (outboundType === 'proxy') target = `Прокси: ${selectedProxy}`;
    else if (outboundType === 'group') {
      if (createNewGroup) target = `Новая группа: ${newGroupName || 'Без имени'} (${newGroupType})`;
      else target = `Группа: ${selectedGroup}`;
    }
    return { matcher, target };
  });

  const step1Valid = $derived(
    (matcherTab === 'catalog' && selectedService !== null) ||
    (matcherTab === 'custom' && customPayload.trim().length > 0)
  );

  const step2Valid = $derived.by(() => {
    if (outboundType === 'direct' || outboundType === 'reject') return true;
    if (outboundType === 'proxy') return selectedProxy.length > 0;
    if (outboundType === 'group') {
      if (createNewGroup) return newGroupName.trim().length > 0 && newGroupMembers.length > 0;
      return selectedGroup.length > 0;
    }
    return false;
  });

  async function handleCreateRule() {
    if (saving || !step1Valid || !step2Valid) return;
    saving = true;
    try {
      let targetOutbound = 'DIRECT';
      if (outboundType === 'reject') {
        targetOutbound = 'REJECT';
      } else if (outboundType === 'proxy') {
        targetOutbound = selectedProxy;
      } else if (outboundType === 'group') {
        if (createNewGroup) {
          const created = await api.mihomoNativeSaveGroup({
            name: newGroupName.trim(),
            type: newGroupType,
            proxies: newGroupMembers,
            use: [],
            url: 'https://www.gstatic.com/generate_204',
            interval: 300,
            lazy: false,
            tolerance: 50,
            enabled: true,
          }, false);
          targetOutbound = created.name;
        } else {
          targetOutbound = selectedGroup;
        }
      }

      const rType = matcherTab === 'catalog' && selectedService ? selectedService.type : customType;
      const rPayload = matcherTab === 'catalog' && selectedService ? selectedService.payload : customPayload.trim();

      await api.mihomoNativeSaveRule({
        type: rType,
        payload: rPayload,
        outbound: targetOutbound,
        enabled: true,
      }, true);

      notifications.success('Правило Mihomo успешно создано и применено');
      onSaved();
      onClose();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка создания правила');
    } finally {
      saving = false;
    }
  }
</script>

<div class="wizard-overlay">
  <div class="wizard-container">
    <div class="wizard-header">
      <div class="wizard-title-row">
        <button type="button" class="back-btn" onclick={onClose} aria-label="Закрыть мастер">
          <ArrowLeft size={16} /> Назад к правилам
        </button>
        <h2>Мастер добавления правила Mihomo</h2>
      </div>
      <div class="stepper">
        <div class="step-pill" class:active={currentStep === 1} class:done={currentStep > 1}>
          <span class="num">1</span> Что маршрутизировать
        </div>
        <div class="step-pill" class:active={currentStep === 2} class:done={currentStep > 2}>
          <span class="num">2</span> Куда направлять
        </div>
        <div class="step-pill" class:active={currentStep === 3}>
          <span class="num">3</span> Применение
        </div>
      </div>
    </div>

    <div class="wizard-body">
      {#if currentStep === 1}
        <div class="step-pane">
          <div class="pane-header">
            <h3>Шаг 1. Выберите сервис или укажите назначение</h3>
            <p class="pane-hint">Выберите готовый сервис из каталога или введите домен / IP-сеть вручную.</p>
          </div>

          <div class="tab-switch">
            <SegmentedControl
              value={matcherTab}
              options={[
                { value: 'catalog', label: 'Каталог сервисов' },
                { value: 'custom', label: 'Вручную (Домен / IP / GeoSite)' }
              ]}
              onchange={(v) => matcherTab = v as typeof matcherTab}
            />
          </div>

          {#if matcherTab === 'catalog'}
            <div class="services-grid">
              {#each CATALOG_SERVICES as svc}
                <button
                  type="button"
                  class="service-card"
                  class:selected={selectedService?.name === svc.name}
                  onclick={() => selectedService = svc}
                >
                  <div class="service-icon">
                    <svc.icon size={22} />
                  </div>
                  <div class="service-info">
                    <span class="service-name">{svc.name}</span>
                    <span class="service-desc">{svc.desc}</span>
                  </div>
                  {#if selectedService?.name === svc.name}
                    <div class="check-badge"><Check size={14} /></div>
                  {/if}
                </button>
              {/each}
            </div>
          {:else}
            <div class="custom-form">
              <label class="form-group">
                <span class="label-text">Тип сопоставления</span>
                <select bind:value={customType} class="select-input">
                  <option value="GEOSITE">GEOSITE (Категория из базы данных, напр. youtube)</option>
                  <option value="DOMAIN-SUFFIX">DOMAIN-SUFFIX (Домен и все его поддомены, напр. google.com)</option>
                  <option value="DOMAIN">DOMAIN (Точное совпадение домена, напр. site.com)</option>
                  <option value="IP-CIDR">IP-CIDR (IPv4/IPv6 подсеть, напр. 91.108.4.0/22)</option>
                  <option value="RULE-SET">RULE-SET (Внешний набор правил из Rule Provider)</option>
                </select>
              </label>

              <label class="form-group">
                <span class="label-text">Значение (Payload)</span>
                <input
                  type="text"
                  bind:value={customPayload}
                  placeholder={customType === 'IP-CIDR' ? '91.108.4.0/22' : customType === 'GEOSITE' ? 'youtube' : 'example.com'}
                  class="text-input"
                />
              </label>
            </div>
          {/if}
        </div>
      {:else if currentStep === 2}
        <div class="step-pane">
          <div class="pane-header">
            <h3>Шаг 2. Куда направлять трафик</h3>
            <p class="pane-hint">Выберите группу прокси для авто-выбора лучшего сервера, конкретный туннель или действие.</p>
          </div>

          <div class="outbound-options">
            <button
              type="button"
              class="outbound-tile"
              class:selected={outboundType === 'group'}
              onclick={() => { outboundType = 'group'; createNewGroup = false; }}
            >
              <div class="tile-icon icon-group"><Zap size={20} /></div>
              <div class="tile-content">
                <strong>Группа прокси (Автовыбор / URL-Test)</strong>
                <span>Mihomo автоматически тестирует задержку и переключает на лучший узел</span>
              </div>
            </button>

            {#if outboundType === 'group'}
              <div class="sub-options">
                {#if availableGroups.length > 0 && !createNewGroup}
                  <label class="form-group">
                    <span class="label-text">Выберите существующую группу</span>
                    <select bind:value={selectedGroup} class="select-input">
                      {#each availableGroups as g}
                        <option value={g.name}>{g.label} ({g.type})</option>
                      {/each}
                    </select>
                  </label>

                  {#if isSelectedGroupLoadBalance}
                    <div class="load-balance-warning">
                      <div class="warning-header">
                        <AlertTriangle size={14} class="warning-icon" />
                        <span class="warning-title">Балансировщик нагрузки ({selectedGroup})</span>
                      </div>
                      <p class="warning-desc">
                        Группа распределяет запросы между разными IP-адресами. Это может вызывать разрыв воспроизведения <strong>YouTube и видеосервисов</strong>. Для видео рекомендуется группа <strong>URL-Test</strong> или <strong>Fallback</strong>.
                      </p>
                    </div>
                  {/if}

                  <Button variant="ghost" size="sm" onclick={() => createNewGroup = true}>
                    <Plus size={14} /> Создать новую группу URL-Test
                  </Button>
                {:else}
                  <div class="new-group-box">
                    <h4>Новая группа авто-выбора (URL-Test)</h4>
                    <label class="form-group">
                      <span class="label-text">Название группы</span>
                      <input type="text" bind:value={newGroupName} placeholder="Например: Media-Fast" class="text-input" />
                    </label>
                    <label class="form-group">
                      <span class="label-text">Серверы / Подписки для группы</span>
                      <div class="members-picker">
                        {#each availableProxies as p}
                          <label class="member-check">
                            <input
                              type="checkbox"
                              checked={newGroupMembers.includes(p.name)}
                              onchange={(e) => {
                                if (e.currentTarget.checked) newGroupMembers = [...newGroupMembers, p.name];
                                else newGroupMembers = newGroupMembers.filter(m => m !== p.name);
                              }}
                            />
                            <span>{p.label}</span>
                          </label>
                        {/each}
                      </div>
                    </label>
                    {#if availableGroups.length > 0}
                      <Button variant="ghost" size="sm" onclick={() => createNewGroup = false}>
                        Отмена, выбрать существующую
                      </Button>
                    {/if}
                  </div>
                {/if}
              </div>
            {/if}

            <button
              type="button"
              class="outbound-tile"
              class:selected={outboundType === 'proxy'}
              onclick={() => outboundType = 'proxy'}
            >
              <div class="tile-icon icon-proxy"><Network size={20} /></div>
              <div class="tile-content">
                <strong>Конкретный прокси / узел</strong>
                <span>Всегда направлять в один постоянный сервер</span>
              </div>
            </button>

            {#if outboundType === 'proxy'}
              <div class="sub-options">
                <label class="form-group">
                  <span class="label-text">Выберите сервер</span>
                  <select bind:value={selectedProxy} class="select-input">
                    {#each availableProxies as p}
                      <option value={p.name}>{p.label}</option>
                    {/each}
                  </select>
                </label>
              </div>
            {/if}

            <button
              type="button"
              class="outbound-tile"
              class:selected={outboundType === 'direct'}
              onclick={() => outboundType = 'direct'}
            >
              <div class="tile-icon icon-direct"><Globe size={20} /></div>
              <div class="tile-content">
                <strong>DIRECT (Прямое подключение)</strong>
                <span>Напрямую через провайдера без VPN и шифрования</span>
              </div>
            </button>

            <button
              type="button"
              class="outbound-tile"
              class:selected={outboundType === 'reject'}
              onclick={() => outboundType = 'reject'}
            >
              <div class="tile-icon icon-reject"><ShieldOff size={20} /></div>
              <div class="tile-content">
                <strong>REJECT (Блокировать)</strong>
                <span>Заблокировать доступ к этим адресам</span>
              </div>
            </button>
          </div>
        </div>
      {:else}
        <div class="step-pane">
          <div class="pane-header">
            <h3>Шаг 3. Подтверждение и создание правила</h3>
            <p class="pane-hint">Проверьте параметры нового маршрута перед применением в Mihomo.</p>
          </div>

          <div class="summary-card">
            <div class="summary-row">
              <span class="summary-label">Что направляем:</span>
              <strong class="summary-val">{ruleSummary.matcher}</strong>
            </div>
            <div class="summary-divider"></div>
            <div class="summary-row">
              <span class="summary-label">Куда направляем:</span>
              <strong class="summary-val highlight">{ruleSummary.target}</strong>
            </div>
          </div>
        </div>
      {/if}
    </div>

    <div class="wizard-footer">
      {#if currentStep > 1}
        <Button variant="secondary" onclick={() => currentStep -= 1} disabled={saving}>
          Назад
        </Button>
      {:else}
        <Button variant="ghost" onclick={onClose} disabled={saving}>
          Отмена
        </Button>
      {/if}

      <div class="footer-actions">
        {#if currentStep < 3}
          <Button
            variant="primary"
            disabled={(currentStep === 1 && !step1Valid) || (currentStep === 2 && !step2Valid)}
            onclick={() => currentStep += 1}
          >
            Далее
          </Button>
        {:else}
          <Button
            variant="primary"
            loading={saving}
            onclick={handleCreateRule}
          >
            Создать и применить
          </Button>
        {/if}
      </div>
    </div>
  </div>
</div>

<style>
  .wizard-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.65);
    backdrop-filter: blur(4px);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    padding: 16px;
  }

  .wizard-container {
    background: var(--bg-surface, #1e1e24);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 12px;
    width: 100%;
    max-width: 680px;
    max-height: 90vh;
    display: flex;
    flex-direction: column;
    box-shadow: 0 16px 32px rgba(0, 0, 0, 0.4);
    overflow: hidden;
  }

  .wizard-header {
    padding: 20px 24px;
    border-bottom: 1px solid var(--border, #2e2e38);
    background: var(--bg-secondary, #252530);
  }

  .wizard-title-row {
    margin-bottom: 16px;
  }

  .back-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: transparent;
    border: none;
    color: var(--text-secondary, #9ba1a6);
    font-size: 13px;
    cursor: pointer;
    margin-bottom: 6px;
    padding: 0;
  }
  .back-btn:hover { color: var(--text-primary, #fff); }

  .wizard-title-row h2 {
    margin: 0;
    font-size: 18px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .stepper {
    display: flex;
    gap: 8px;
  }

  .step-pill {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px;
    border-radius: 6px;
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    color: var(--text-secondary, #9ba1a6);
    font-size: 12px;
  }

  .step-pill.active {
    background: var(--accent-bg, rgba(59, 130, 246, 0.15));
    border-color: var(--accent, #3b82f6);
    color: var(--text-primary, #fff);
    font-weight: 500;
  }

  .step-pill.done {
    border-color: var(--color-success, #10b981);
    color: var(--text-primary, #fff);
  }

  .step-pill .num {
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: rgba(255, 255, 255, 0.1);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
  }

  .step-pill.active .num {
    background: var(--accent, #3b82f6);
    color: #fff;
  }

  .wizard-body {
    padding: 24px;
    overflow-y: auto;
    flex: 1;
  }

  .pane-header {
    margin-bottom: 20px;
  }

  .pane-header h3 {
    margin: 0 0 4px;
    font-size: 16px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .pane-hint {
    margin: 0;
    font-size: 13px;
    color: var(--text-secondary, #9ba1a6);
  }

  .tab-switch {
    margin-bottom: 16px;
  }

  .services-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 10px;
  }

  .service-card {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 12px;
    border-radius: 8px;
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    cursor: pointer;
    text-align: left;
    transition: all 0.15s ease;
    position: relative;
  }

  .service-card:hover {
    border-color: var(--border-hover, #4a4a58);
    background: var(--bg-hover, #2c2c38);
  }

  .service-card.selected {
    border-color: var(--accent, #3b82f6);
    background: rgba(59, 130, 246, 0.12);
  }

  .service-icon {
    color: var(--accent, #3b82f6);
    flex-shrink: 0;
    margin-top: 2px;
  }

  .service-info {
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .service-name {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .service-desc {
    font-size: 11px;
    color: var(--text-muted, #71717a);
    margin-top: 2px;
    line-height: 1.3;
  }

  .check-badge {
    position: absolute;
    top: 8px;
    right: 8px;
    color: var(--accent, #3b82f6);
  }

  .custom-form {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .form-group {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .label-text {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-secondary, #9ba1a6);
  }

  .select-input, .text-input {
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 6px;
    padding: 8px 12px;
    color: var(--text-primary, #fff);
    font-size: 14px;
    outline: none;
  }

  .select-input:focus, .text-input:focus {
    border-color: var(--accent, #3b82f6);
  }

  .outbound-options {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .outbound-tile {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px;
    border-radius: 8px;
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    cursor: pointer;
    text-align: left;
    transition: all 0.15s ease;
  }

  .outbound-tile:hover {
    border-color: var(--border-hover, #4a4a58);
  }

  .outbound-tile.selected {
    border-color: var(--accent, #3b82f6);
    background: rgba(59, 130, 246, 0.12);
  }

  .tile-icon {
    width: 36px;
    height: 36px;
    border-radius: 8px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }

  .icon-group { background: rgba(59, 130, 246, 0.2); color: #3b82f6; }
  .icon-proxy { background: rgba(168, 85, 247, 0.2); color: #a855f7; }
  .icon-direct { background: rgba(16, 185, 129, 0.2); color: #10b981; }
  .icon-reject { background: rgba(239, 68, 68, 0.2); color: #ef4444; }

  .tile-content {
    display: flex;
    flex-direction: column;
  }

  .tile-content strong {
    font-size: 14px;
    color: var(--text-primary, #fff);
  }

  .tile-content span {
    font-size: 12px;
    color: var(--text-secondary, #9ba1a6);
    margin-top: 2px;
  }

  .sub-options {
    padding: 12px 16px;
    margin-top: -4px;
    margin-bottom: 6px;
    border-radius: 0 0 8px 8px;
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    border-top: none;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .new-group-box {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .new-group-box h4 {
    margin: 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .members-picker {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 140px;
    overflow-y: auto;
    padding: 6px;
    border: 1px solid var(--border, #2e2e38);
    border-radius: 6px;
    background: var(--bg-secondary, #252530);
  }

  .member-check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text-primary, #fff);
    cursor: pointer;
  }

  .summary-card {
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 8px;
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .summary-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 14px;
  }

  .summary-label {
    color: var(--text-secondary, #9ba1a6);
  }

  .summary-val {
    color: var(--text-primary, #fff);
  }

  .summary-val.highlight {
    color: var(--accent, #3b82f6);
    font-size: 15px;
  }

  .summary-divider {
    height: 1px;
    background: var(--border, #2e2e38);
  }

  .wizard-footer {
    padding: 16px 24px;
    border-top: 1px solid var(--border, #2e2e38);
    background: var(--bg-secondary, #252530);
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .footer-actions {
    display: flex;
    gap: 10px;
  }

  .load-balance-warning {
    background: rgba(217, 119, 6, 0.05);
    border: 1px solid rgba(217, 119, 6, 0.35);
    border-radius: var(--radius-md, 8px);
    padding: 8px 12px;
    font-size: 12px;
    line-height: 1.4;
    margin: 6px 0 10px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .warning-header {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  :global(.warning-icon) {
    color: var(--color-warning, #d97706);
    flex-shrink: 0;
  }

  .warning-title {
    color: var(--color-warning, #d97706);
    font-weight: 600;
    font-size: 12.5px;
  }

  .warning-desc {
    margin: 0;
    color: var(--text-secondary, #9ba1a6);
  }
</style>
