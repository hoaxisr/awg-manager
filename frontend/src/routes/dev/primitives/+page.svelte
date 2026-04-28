<script lang="ts">
  import './_styles.css';
  import { Button, IconButton, Card, Badge, StatusDot, Tabs, LegacyTabs, LegacyTab, Input, Dropdown, Toggle, Modal } from '$lib/components/ui';
  import { LogsLiveIndicator, LogRow, LogsToolbar } from '$lib/components/diagnostics';

  let theme = $state<'dark' | 'light'>('dark');

  $effect(() => {
    document.documentElement.classList.toggle('light', theme === 'light');
    return () => {
      document.documentElement.classList.remove('light');
    };
  });

  let tabUnderline = $state('tunnels');
  let tabPill = $state('basic');
  let tabCanonical = $state('logs');
  const canonicalTabsDemo = [
    { id: 'logs', label: 'Журнал' },
    { id: 'connections', label: 'Соединения', badge: 47 },
    { id: 'checks', label: 'Проверки' },
  ];

  let inputBasic = $state('');
  let inputErr = $state('home');
  let inputDisabled = $state('Hf3...G7m=');
  let inputAffix = $state('10.66.66.1');

  let selectVal = $state('');
  let selectDisabled = $state('kernel');
  let selectErr = $state('');

  let toggleA = $state(true);
  let toggleB = $state(false);
  let toggleC = $state(false);
  let toggleD = $state(false);
  let modalOpen = $state(false);

  const sampleLogs = [
    { timestamp: new Date().toISOString(), level: 'info', group: 'tunnel', subgroup: 'lifecycle', action: 'start', target: 'tunnel-a', message: 'Tunnel started successfully' },
    { timestamp: new Date().toISOString(), level: 'warn', group: 'routing', subgroup: 'dns-route', action: 'failover', target: 'netflix', message: 'Switched tunnel-a → tunnel-b after 3 failed pings' },
    { timestamp: new Date().toISOString(), level: 'error', group: 'system', subgroup: 'wan', action: 'down', target: 'wan-isp', message: 'Default route lost' },
    { timestamp: new Date().toISOString(), level: 'debug', group: 'tunnel', subgroup: 'state', action: 'transition', target: 'tunnel-c', message: 'Pending → Suspended' },
  ];
  let logExpanded = $state<Record<number, boolean>>({});

  let toolbarFilter = $state({ search: '', group: '', subgroup: '', levels: ['error', 'warn', 'info', 'full', 'debug'] });
  let toolbarPaused = $state(false);
</script>

<svelte:head>
  <title>Primitives — Design System</title>
</svelte:head>

<div class="dev-page">
  <header class="dev-header">
    <h1 class="dev-title">Primitives</h1>
    <div class="dev-theme-toggle" role="tablist" aria-label="Theme">
      <button
        type="button"
        class:active={theme === 'dark'}
        onclick={() => (theme = 'dark')}
        role="tab"
        aria-selected={theme === 'dark'}
      >Dark</button>
      <button
        type="button"
        class:active={theme === 'light'}
        onclick={() => (theme = 'light')}
        role="tab"
        aria-selected={theme === 'light'}
      >Light</button>
    </div>
  </header>

  <nav class="dev-nav" aria-label="Primitives sections">
    <a href="#button">Button</a>
    <a href="#icon-button">IconButton</a>
    <a href="#card">Card</a>
    <a href="#badge">Badge</a>
    <a href="#status-dot">StatusDot</a>
    <a href="#tabs">Tabs</a>
    <a href="#input">Input</a>
    <a href="#select">Dropdown</a>
    <a href="#toggle">Toggle</a>
    <a href="#modal">Modal</a>
    <a href="#logs-live-indicator">LogsLiveIndicator</a>
    <a href="#log-row">LogRow</a>
    <a href="#logs-toolbar">LogsToolbar</a>
  </nav>

  <section id="button" class="dev-section">
    <h2 class="dev-section-title">Button</h2>

    <div class="dev-row">
      <span class="dev-row-label">Variants (size sm)</span>
      <Button variant="primary">Primary</Button>
      <Button variant="secondary">Secondary</Button>
      <Button variant="ghost">Ghost</Button>
      <Button variant="danger">Danger</Button>
      <Button variant="success">Success</Button>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Variants (size md)</span>
      <Button variant="primary" size="md">Primary</Button>
      <Button variant="secondary" size="md">Secondary</Button>
      <Button variant="ghost" size="md">Ghost</Button>
      <Button variant="danger" size="md">Danger</Button>
      <Button variant="success" size="md">Success</Button>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">States</span>
      <Button variant="primary" disabled>Disabled</Button>
      <Button variant="primary" loading>Loading</Button>
      <Button variant="secondary" disabled>Secondary disabled</Button>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">As link (href)</span>
      <Button variant="primary" href="#button">Link Primary</Button>
      <Button variant="ghost" href="#button">Link Ghost</Button>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Full width</span>
      <Button variant="primary" fullWidth>Full Width Primary</Button>
    </div>
  </section>

  <section id="icon-button" class="dev-section">
    <h2 class="dev-section-title">IconButton</h2>

    <div class="dev-row">
      <span class="dev-row-label">Variants (sm)</span>
      <IconButton ariaLabel="Default action">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v8M8 12h8"/></svg>
      </IconButton>
      <IconButton variant="danger" ariaLabel="Logout">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/></svg>
      </IconButton>
      <IconButton variant="warm" ariaLabel="Donate">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 1 0-7.78 7.78L12 21.23l8.84-8.84a5.5 5.5 0 0 0 0-7.78z"/></svg>
      </IconButton>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Disabled</span>
      <IconButton ariaLabel="Disabled" disabled>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v8M8 12h8"/></svg>
      </IconButton>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Size md</span>
      <IconButton size="md" ariaLabel="Settings">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h.01a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.01a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
      </IconButton>
    </div>
  </section>

  <section id="card" class="dev-section">
    <h2 class="dev-section-title">Card</h2>

    <div class="dev-row">
      <span class="dev-row-label">Default (with header & footer)</span>
      <div style="width: 320px;">
        <Card>
          {#snippet header()}
            <strong>Card title</strong>
            <Button variant="ghost" size="sm">Action</Button>
          {/snippet}
          Body content goes here. This is the default card variant with shadow and border.
          {#snippet footer()}
            <small style="color: var(--color-text-muted);">Footer area for metadata</small>
          {/snippet}
        </Card>
      </div>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Nested (no shadow)</span>
      <div style="width: 320px;">
        <Card variant="nested">Nested card without shadow — for cards inside cards.</Card>
      </div>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Padding variants</span>
      <div style="width: 240px;">
        <Card padding="sm">Padding sm</Card>
      </div>
      <div style="width: 240px;">
        <Card padding="md">Padding md (default)</Card>
      </div>
      <div style="width: 240px;">
        <Card padding="lg">Padding lg</Card>
      </div>
    </div>
  </section>

  <section id="badge" class="dev-section">
    <h2 class="dev-section-title">Badge</h2>

    <div class="dev-row">
      <span class="dev-row-label">Variants (sm)</span>
      <Badge>Default</Badge>
      <Badge variant="accent">Accent</Badge>
      <Badge variant="success">Connected</Badge>
      <Badge variant="error">Failed</Badge>
      <Badge variant="warning">Degraded</Badge>
      <Badge variant="info">Info</Badge>
      <Badge variant="muted">Muted</Badge>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Variants (md)</span>
      <Badge size="md">Default</Badge>
      <Badge variant="accent" size="md">Accent</Badge>
      <Badge variant="success" size="md">Connected</Badge>
      <Badge variant="error" size="md">Failed</Badge>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Protocol style (uppercase + mono)</span>
      <Badge variant="accent" uppercase mono>tcp</Badge>
      <Badge variant="success" uppercase mono>udp</Badge>
      <Badge variant="warning" uppercase mono>icmp</Badge>
    </div>
  </section>

  <section id="status-dot" class="dev-section">
    <h2 class="dev-section-title">StatusDot</h2>

    <div class="dev-row">
      <span class="dev-row-label">Variants (md)</span>
      <span style="display:flex; gap:0.5rem; align-items:center;"><StatusDot variant="success" /> Success</span>
      <span style="display:flex; gap:0.5rem; align-items:center;"><StatusDot variant="error" /> Error</span>
      <span style="display:flex; gap:0.5rem; align-items:center;"><StatusDot variant="warning" /> Warning</span>
      <span style="display:flex; gap:0.5rem; align-items:center;"><StatusDot variant="info" /> Info</span>
      <span style="display:flex; gap:0.5rem; align-items:center;"><StatusDot variant="muted" /> Muted</span>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Pulsing</span>
      <span style="display:flex; gap:0.5rem; align-items:center;"><StatusDot variant="success" pulse /> Active</span>
      <span style="display:flex; gap:0.5rem; align-items:center;"><StatusDot variant="warning" pulse /> Degraded</span>
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Size sm</span>
      <span style="display:flex; gap:0.5rem; align-items:center;"><StatusDot variant="success" size="sm" /> Small</span>
    </div>
  </section>

  <section id="tabs" class="dev-section">
    <h2 class="dev-section-title">Tabs</h2>

    <div class="dev-row" style="flex-direction: column; align-items: stretch;">
      <span class="dev-row-label">Tabs — canonical primitive (chip + auto-overflow + badge)</span>
      <Tabs
        tabs={canonicalTabsDemo}
        active={tabCanonical}
        onchange={(id) => (tabCanonical = id)}
      />
      <p style="color: var(--color-text-muted); font-size: 12px; margin-top: 0.5rem;">Active: {tabCanonical}</p>
    </div>

    <div class="dev-row" style="flex-direction: column; align-items: stretch;">
      <span class="dev-row-label">LegacyTabs — underline variant (deprecated, used by AppHeader only)</span>
      <LegacyTabs bind:value={tabUnderline} variant="underline">
        <LegacyTab value="tunnels">Туннели</LegacyTab>
        <LegacyTab value="routing">Маршрутизация</LegacyTab>
        <LegacyTab value="monitoring">Мониторинг</LegacyTab>
        <LegacyTab value="diagnostics">Диагностика</LegacyTab>
      </LegacyTabs>
      <p style="color: var(--color-text-muted); font-size: 12px; margin-top: 0.5rem;">Active: {tabUnderline}</p>
    </div>

    <div class="dev-row" style="flex-direction: column; align-items: stretch;">
      <span class="dev-row-label">LegacyTabs — pill variant (deprecated)</span>
      <LegacyTabs bind:value={tabPill} variant="pill">
        <LegacyTab value="basic">Основное</LegacyTab>
        <LegacyTab value="obfuscation">Обфускация</LegacyTab>
        <LegacyTab value="routing">Маршрутизация</LegacyTab>
      </LegacyTabs>
      <p style="color: var(--color-text-muted); font-size: 12px; margin-top: 0.5rem;">Active: {tabPill}</p>
    </div>
  </section>

  <section id="input" class="dev-section">
    <h2 class="dev-section-title">Input</h2>

    <div class="dev-row" style="flex-direction: column; align-items: flex-start; max-width: 400px;">
      <span class="dev-row-label">Basic + label + hint</span>
      <Input bind:value={inputBasic} label="Эндпоинт" placeholder="example.com:51820" hint="IP или FQDN, опционально с портом" fullWidth />
    </div>

    <div class="dev-row" style="flex-direction: column; align-items: flex-start; max-width: 400px;">
      <span class="dev-row-label">Required + error state</span>
      <Input bind:value={inputErr} label="Имя туннеля" placeholder="my-tunnel" error="Имя уже занято" required fullWidth />
    </div>

    <div class="dev-row" style="flex-direction: column; align-items: flex-start; max-width: 400px;">
      <span class="dev-row-label">Disabled</span>
      <Input bind:value={inputDisabled} label="Public key" disabled fullWidth />
    </div>

    <div class="dev-row" style="flex-direction: column; align-items: flex-start; max-width: 400px;">
      <span class="dev-row-label">With prefix and suffix</span>
      <Input bind:value={inputAffix} label="Адрес" placeholder="10.66.66.1" fullWidth>
        {#snippet prefix()}IPv4{/snippet}
        {#snippet suffix()}/24{/snippet}
      </Input>
    </div>
  </section>

  <section id="select" class="dev-section">
    <h2 class="dev-section-title">Dropdown</h2>

    <div class="dev-row" style="flex-direction: column; align-items: flex-start; max-width: 400px;">
      <span class="dev-row-label">Basic with placeholder</span>
      <Dropdown
        bind:value={selectVal}
        options={[
          { value: 'tunnel-a', label: 'tunnel-a (NL Rotterdam)' },
          { value: 'tunnel-b', label: 'tunnel-b (DE Frankfurt)' },
          { value: 'tunnel-c', label: 'tunnel-c (US East)' }
        ]}
        label="Outbound туннель"
        placeholder="— выберите —"
        hint="Используется для маршрутизации"
        fullWidth
      />
    </div>

    <div class="dev-row" style="flex-direction: column; align-items: flex-start; max-width: 400px;">
      <span class="dev-row-label">Disabled option</span>
      <Dropdown
        bind:value={selectDisabled}
        options={[
          { value: 'kernel', label: 'Kernel module' },
          { value: 'userspace', label: 'Userspace (amneziawg-go)', disabled: true }
        ]}
        label="Backend"
        fullWidth
      />
    </div>

    <div class="dev-row" style="flex-direction: column; align-items: flex-start; max-width: 400px;">
      <span class="dev-row-label">Error state</span>
      <Dropdown
        bind:value={selectErr}
        options={[
          { value: 'a', label: 'A' },
          { value: 'b', label: 'B' }
        ]}
        label="Регион"
        error="Регион обязателен"
        required
        placeholder="—"
        fullWidth
      />
    </div>
  </section>

  <section id="toggle" class="dev-section">
    <h2 class="dev-section-title">Toggle</h2>

    <div class="dev-row">
      <span class="dev-row-label">States</span>
      <Toggle checked={toggleA} onchange={(v) => (toggleA = v)} label="Включён" />
      <Toggle checked={toggleB} onchange={(v) => (toggleB = v)} label="Выключен" />
      <Toggle checked={toggleC} onchange={(v) => (toggleC = v)} loading label="Loading" />
      <Toggle checked={toggleD} onchange={(v) => (toggleD = v)} disabled label="Disabled" />
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Sizes</span>
      <Toggle checked={toggleA} onchange={(v) => (toggleA = v)} size="sm" label="Small" />
      <Toggle checked={toggleA} onchange={(v) => (toggleA = v)} size="md" label="Medium" />
    </div>

    <div class="dev-row">
      <span class="dev-row-label">Flip variant</span>
      <Toggle checked={toggleA} onchange={(v) => (toggleA = v)} variant="flip" />
    </div>
  </section>

  <section id="modal" class="dev-section">
    <h2 class="dev-section-title">Modal</h2>

    <div class="dev-row">
      <span class="dev-row-label">Trigger</span>
      <Button variant="primary" onclick={() => (modalOpen = true)}>Открыть модалку</Button>
    </div>

    <Modal
      open={modalOpen}
      title="Подтверждение"
      onclose={() => (modalOpen = false)}
    >
      <p>Это пример модального окна. Корпус, header, footer.</p>
      <p style="color: var(--color-text-muted); font-size: 12px; margin-top: 0.5rem;">Esc и клик вне закрывают.</p>
      {#snippet actions()}
        <Button variant="ghost" onclick={() => (modalOpen = false)}>Отмена</Button>
        <Button variant="primary" onclick={() => (modalOpen = false)}>OK</Button>
      {/snippet}
    </Modal>
  </section>

  <section id="logs-live-indicator" class="dev-section">
    <h2 class="dev-section-title">LogsLiveIndicator</h2>

    <div class="dev-row">
      <span class="dev-row-label">LIVE</span>
      <LogsLiveIndicator paused={false} bufferCount={0} entries={1234} />
    </div>

    <div class="dev-row">
      <span class="dev-row-label">PAUSED, no buffer</span>
      <LogsLiveIndicator paused={true} bufferCount={0} entries={1234} />
    </div>

    <div class="dev-row">
      <span class="dev-row-label">PAUSED with 5 buffered</span>
      <LogsLiveIndicator paused={true} bufferCount={5} entries={1234} onResume={() => alert('resume')} />
    </div>
  </section>

  <section id="log-row" class="dev-section">
    <h2 class="dev-section-title">LogRow</h2>

    <div class="dev-row" style="flex-direction: column; align-items: stretch;">
      <span class="dev-row-label">Mixed levels (error/warn auto-expand, others click to toggle)</span>
      {#each sampleLogs as log, i}
        <LogRow
          {log}
          expanded={logExpanded[i] ?? false}
          onToggleExpand={() => (logExpanded = { ...logExpanded, [i]: !logExpanded[i] })}
          onClickScope={(g, sg) => alert(`filter scope ${g}/${sg}`)}
          onClickLevel={(lv) => alert(`filter level ${lv}`)}
        />
      {/each}
    </div>
  </section>

  <section id="logs-toolbar" class="dev-section">
    <h2 class="dev-section-title">LogsToolbar</h2>

    <div class="dev-row" style="flex-direction: column; align-items: stretch;">
      <span class="dev-row-label">Default state</span>
      <LogsToolbar
        bind:filter={toolbarFilter}
        onFilterChange={(f) => console.log('filter', f)}
        paused={toolbarPaused}
        bufferCount={0}
        onTogglePause={() => toolbarPaused = !toolbarPaused}
        onResume={() => toolbarPaused = false}
        onCopy={() => alert('copy')}
        onDownload={() => alert('download')}
        onClear={() => alert('clear')}
        totalEntries={1234}
        visibleEntries={567}
      />
    </div>
  </section>
</div>
