<script lang="ts">
  import { m } from '$lib/i18n';
  import type { FircSettingValue } from '$lib/types/firc';
  import { Toggle } from '$lib/components/ui';

  interface Props {
    settings: Record<string, FircSettingValue>;
    onfield: (key: string, val: FircSettingValue) => void;
  }

  let { settings, onfield }: Props = $props();

  function strVal(key: string, fallback = ''): string {
    const v = settings[key];
    if (v === undefined || v === null) return fallback;
    return String(v);
  }

  function numVal(key: string, fallback = 0): number {
    const v = settings[key];
    if (typeof v === 'number' && Number.isFinite(v)) return v;
    if (typeof v !== 'string' || v.trim() === '') return fallback;
    const parsed = Number(v);
    return Number.isFinite(parsed) ? parsed : fallback;
  }

  function boolVal(key: string, fallback = false): boolean {
    const v = settings[key];
    if (typeof v === 'boolean') return v;
    if (v === 'true' || v === 1) return true;
    if (v === 'false' || v === 0) return false;
    return fallback;
  }

  function setField(key: string, val: FircSettingValue) {
    onfield(key, val);
  }

  function invertedBool(key: string): boolean {
    return !boolVal(key, false);
  }

  function setInvertedBool(key: string, enabled: boolean) {
    setField(key, !enabled);
  }

  let lanLinksText = $derived.by(() => {
    const raw = settings['app.link'];
    if (Array.isArray(raw)) return raw.join(', ');
    if (typeof raw === 'string') return raw;
    return 'br0';
  });

  function updateLanLinks(text: string) {
    const list = text
      .split(/[\s,;]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    setField('app.link', list);
  }
</script>

<!-- 1. DNS-прокси -->
<section class="sec">
  <div class="sec-cap">{m.firc_settings_dns_title()}</div>
  <span class="hint">
    {m.firc_settings_dns_hint()}
  </span>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-dns-up-addr">{m.firc_settings_dns_upstream_addr()}</label>
      <input
        id="firc-dns-up-addr"
        class="inp mono"
        type="text"
        placeholder="127.0.0.1"
        value={strVal('app.dnsProxy.upstream.address', '127.0.0.1')}
        oninput={(e) => setField('app.dnsProxy.upstream.address', (e.currentTarget as HTMLInputElement).value)}
      />
    </div>
    <div class="field">
      <label class="lbl" for="firc-dns-up-port">{m.firc_settings_dns_upstream_port()}</label>
      <input
        id="firc-dns-up-port"
        class="inp mono"
        type="number"
        min="1"
        max="65535"
        value={numVal('app.dnsProxy.upstream.port', 53)}
        oninput={(e) => setField('app.dnsProxy.upstream.port', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
  </div>
  <span class="hint">{m.firc_settings_dns_upstream_hint()}</span>

  <div class="field-row">
    <div class="field-row-text">
      <span class="lbl">{m.firc_settings_dns_drop_aaaa()}</span>
      <span class="hint">{m.firc_settings_dns_drop_aaaa_hint()}</span>
    </div>
    <Toggle
      checked={invertedBool('app.dnsProxy.disableDropAAAA')}
      onchange={(v) => setInvertedBool('app.dnsProxy.disableDropAAAA', v)}
    />
  </div>

  <div class="field-row">
    <div class="field-row-text">
      <span class="lbl">{m.firc_settings_dns_remap53()}</span>
      <span class="hint">{m.firc_settings_dns_remap53_hint()}</span>
    </div>
    <Toggle
      checked={invertedBool('app.dnsProxy.disableRemap53')}
      onchange={(v) => setInvertedBool('app.dnsProxy.disableRemap53', v)}
    />
  </div>

  <div class="field-row">
    <div class="field-row-text">
      <span class="lbl">{m.firc_settings_dns_stats()}</span>
      <span class="hint">{m.firc_settings_dns_stats_hint()}</span>
    </div>
    <Toggle
      checked={invertedBool('app.dnsProxy.disableStats')}
      onchange={(v) => setInvertedBool('app.dnsProxy.disableStats', v)}
    />
  </div>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-dns-host-addr">{m.firc_settings_dns_listen_addr()}</label>
      <input
        id="firc-dns-host-addr"
        class="inp mono"
        type="text"
        placeholder="0.0.0.0"
        value={strVal('app.dnsProxy.host.address', '0.0.0.0')}
        oninput={(e) => setField('app.dnsProxy.host.address', (e.currentTarget as HTMLInputElement).value)}
      />
    </div>
    <div class="field">
      <label class="lbl" for="firc-dns-host-port">{m.firc_settings_dns_listen_port()}</label>
      <input
        id="firc-dns-host-port"
        class="inp mono"
        type="number"
        min="1"
        max="65535"
        value={numVal('app.dnsProxy.host.port', 3553)}
        oninput={(e) => setField('app.dnsProxy.host.port', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
  </div>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-dns-ttl">{m.firc_settings_dns_unmatched_ttl()}</label>
      <input
        id="firc-dns-ttl"
        class="inp mono"
        type="number"
        min="0"
        max="86400"
        value={numVal('app.dnsProxy.unmatchedTtl', 60)}
        oninput={(e) => setField('app.dnsProxy.unmatchedTtl', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
    <div class="field">
      <label class="lbl" for="firc-dns-timeout">{m.firc_settings_dns_timeout()}</label>
      <input
        id="firc-dns-timeout"
        class="inp mono"
        type="number"
        min="100"
        max="30000"
        value={numVal('app.dnsProxy.timeout', 5000)}
        oninput={(e) => setField('app.dnsProxy.timeout', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
  </div>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-dns-conc">{m.firc_settings_dns_max_concurrent()}</label>
      <input
        id="firc-dns-conc"
        class="inp mono"
        type="number"
        min="1"
        max="4096"
        value={numVal('app.dnsProxy.maxConcurrent', 256)}
        oninput={(e) => setField('app.dnsProxy.maxConcurrent', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
    <div class="field">
      <label class="lbl" for="firc-dns-idle">{m.firc_settings_dns_idle_conns()}</label>
      <input
        id="firc-dns-idle"
        class="inp mono"
        type="number"
        min="0"
        max="512"
        value={numVal('app.dnsProxy.maxIdleConns', 32)}
        oninput={(e) => setField('app.dnsProxy.maxIdleConns', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
  </div>
</section>

<!-- 2. Пул адресов (FakeIP) -->
<section class="sec">
  <div class="sec-cap">{m.firc_settings_pool_title()}</div>
  <span class="hint">
    {m.firc_settings_pool_hint()}
  </span>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-pool-ttl">{m.firc_settings_pool_ttl()}</label>
      <input
        id="firc-pool-ttl"
        class="inp mono"
        type="number"
        min="1"
        max="15724800"
        value={numVal('app.addressPool.ttlClamp', 300)}
        oninput={(e) => setField('app.addressPool.ttlClamp', Number((e.currentTarget as HTMLInputElement).value))}
      />
      <span class="hint">{m.firc_settings_pool_ttl_hint()}</span>
    </div>
    <div class="field">
      <label class="lbl" for="firc-pool-max">{m.firc_settings_pool_max()}</label>
      <input
        id="firc-pool-max"
        class="inp mono"
        type="number"
        min="256"
        max="262144"
        value={numVal('app.addressPool.maxNames', 65536)}
        oninput={(e) => setField('app.addressPool.maxNames', Number((e.currentTarget as HTMLInputElement).value))}
      />
      <span class="hint">{m.firc_settings_pool_max_hint()}</span>
    </div>
  </div>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-pool-v4">{m.firc_settings_pool_v4()}</label>
      <input
        id="firc-pool-v4"
        class="inp mono"
        type="text"
        placeholder="10.255.0.0/16"
        value={strVal('app.addressPool.v4.pool', '10.255.0.0/16')}
        oninput={(e) => setField('app.addressPool.v4.pool', (e.currentTarget as HTMLInputElement).value)}
      />
    </div>
    <div class="field">
      <label class="lbl" for="firc-pool-v4-chunk">{m.firc_settings_pool_v4_chunk()}</label>
      <input
        id="firc-pool-v4-chunk"
        class="inp mono"
        type="number"
        min="8"
        max="32"
        value={numVal('app.addressPool.v4.chunk', 24)}
        oninput={(e) => setField('app.addressPool.v4.chunk', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
  </div>
  <span class="hint">{m.firc_settings_pool_range_hint()}</span>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-pool-v6">{m.firc_settings_pool_v6()}</label>
      <input
        id="firc-pool-v6"
        class="inp mono"
        type="text"
        placeholder={m.firc_settings_pool_v6_auto()}
        value={strVal('app.addressPool.v6.pool', '')}
        oninput={(e) => setField('app.addressPool.v6.pool', (e.currentTarget as HTMLInputElement).value)}
      />
    </div>
    <div class="field">
      <label class="lbl" for="firc-pool-v6-chunk">{m.firc_settings_pool_v6_chunk()}</label>
      <input
        id="firc-pool-v6-chunk"
        class="inp mono"
        type="number"
        min="32"
        max="128"
        value={numVal('app.addressPool.v6.chunk', 64)}
        oninput={(e) => setField('app.addressPool.v6.chunk', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
  </div>
</section>

<!-- 3. Netfilter и маршрутизация -->
<section class="sec">
  <div class="sec-cap">{m.firc_settings_nf_title()}</div>
  <span class="hint">
    {m.firc_settings_nf_hint()}
  </span>

  <div class="field-row">
    <div class="field-row-text">
      <span class="lbl">{m.firc_settings_nf_ipv4()}</span>
      <span class="hint">{m.firc_settings_nf_ipv4_hint()}</span>
    </div>
    <Toggle
      checked={invertedBool('app.netfilter.disableIPv4')}
      onchange={(v) => setInvertedBool('app.netfilter.disableIPv4', v)}
    />
  </div>

  <div class="field-row">
    <div class="field-row-text">
      <span class="lbl">{m.firc_settings_nf_ipv6()}</span>
      <span class="hint">{m.firc_settings_nf_ipv6_hint()}</span>
    </div>
    <Toggle
      checked={invertedBool('app.netfilter.disableIPv6')}
      onchange={(v) => setInvertedBool('app.netfilter.disableIPv6', v)}
    />
  </div>

  <div class="field">
    <label class="lbl" for="firc-lan-links">{m.firc_settings_nf_lan_links()}</label>
    <input
      id="firc-lan-links"
      class="inp mono"
      type="text"
      placeholder="br0"
      value={lanLinksText}
      oninput={(e) => updateLanLinks((e.currentTarget as HTMLInputElement).value)}
    />
    <span class="hint">{m.firc_settings_nf_lan_links_hint()}</span>
  </div>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-nf-prefix">{m.firc_settings_nf_chain_prefix()}</label>
      <input
        id="firc-nf-prefix"
        class="inp mono"
        type="text"
        placeholder="FIRC_"
        value={strVal('app.netfilter.iptables.chainPrefix', 'FIRC_')}
        oninput={(e) => setField('app.netfilter.iptables.chainPrefix', (e.currentTarget as HTMLInputElement).value)}
      />
    </div>
    <div class="field">
      <label class="lbl" for="firc-nf-mark">{m.firc_settings_nf_start_mark()}</label>
      <input
        id="firc-nf-mark"
        class="inp mono"
        type="number"
        min="100"
        max="60000"
        value={numVal('app.netfilter.startMarkTableIndex', 2100)}
        oninput={(e) => setField('app.netfilter.startMarkTableIndex', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
  </div>
</section>

<!-- 4. Проверка интерфейсов -->
<section class="sec">
  <div class="sec-cap">{m.firc_settings_chk_title()}</div>
  <span class="hint">
    {m.firc_settings_chk_hint()}
  </span>

  <div class="field">
    <label class="lbl" for="firc-chk-url">{m.firc_settings_chk_url()}</label>
    <input
      id="firc-chk-url"
      class="inp mono"
      type="text"
      placeholder="http://cp.cloudflare.com/generate_204"
      value={strVal('app.check.url', 'http://cp.cloudflare.com/generate_204')}
      oninput={(e) => setField('app.check.url', (e.currentTarget as HTMLInputElement).value)}
    />
    <span class="hint">{m.firc_settings_chk_url_hint()}</span>
  </div>

  <div class="field">
    <label class="lbl" for="firc-chk-wan">{m.firc_settings_chk_wan_url()}</label>
    <input
      id="firc-chk-wan"
      class="inp mono"
      type="text"
      placeholder="http://api.browser.yandex.ru/generate_204"
      value={strVal('app.check.wanUrl', 'http://api.browser.yandex.ru/generate_204')}
      oninput={(e) => setField('app.check.wanUrl', (e.currentTarget as HTMLInputElement).value)}
    />
    <span class="hint">{m.firc_settings_chk_wan_url_hint()}</span>
  </div>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="firc-chk-int">{m.firc_settings_chk_interval()}</label>
      <input
        id="firc-chk-int"
        class="inp mono"
        type="number"
        min="10"
        max="3600"
        value={numVal('app.check.interval', 60)}
        oninput={(e) => setField('app.check.interval', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
    <div class="field">
      <label class="lbl" for="firc-chk-to">{m.firc_settings_chk_timeout()}</label>
      <input
        id="firc-chk-to"
        class="inp mono"
        type="number"
        min="1"
        max="30"
        value={numVal('app.check.timeout', 5)}
        oninput={(e) => setField('app.check.timeout', Number((e.currentTarget as HTMLInputElement).value))}
      />
    </div>
  </div>
</section>

<style>
  .sec {
    padding: 14px 14px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg-secondary);
    display: flex;
    flex-direction: column;
    gap: 12px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    overflow: hidden;
  }

  .sec-cap {
    font-size: 11px;
    font-weight: 700;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
    min-width: 0;
    max-width: 100%;
  }

  .field-grid-2 {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 10px;
    min-width: 0;
    max-width: 100%;
  }

  @media (max-width: 560px) {
    .field-grid-2 {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .field-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 0;
    border-top: 1px solid rgba(255, 255, 255, 0.04);
    min-width: 0;
    max-width: 100%;
  }

  .field-row-text {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    flex: 1 1 auto;
  }

  .lbl {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary);
  }

  .inp {
    padding: 7px 10px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-primary);
    font-size: 12px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    transition: border-color 0.15s;
  }

  .inp:focus {
    outline: none;
    border-color: var(--accent);
  }

  .mono {
    font-family: var(--font-mono);
  }

  .hint {
    font-size: 11px;
    color: var(--text-secondary);
    line-height: 1.4;
  }
</style>
