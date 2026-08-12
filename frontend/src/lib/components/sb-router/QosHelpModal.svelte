<!--
  «Как пометить трафик на ПК» — компактная справка для QoS/DSCP-классов
  (issue #371). PowerShell-политика QoS с подставленным DSCP выбранного
  класса, reg add для недоменных/мульти-NIC машин и короткие заметки про
  Linux / macOS / Wi-Fi WMM.
-->
<script lang="ts">
  import { Copy } from 'lucide-svelte';
  import { Modal, IconButton } from '$lib/components/ui';
  import { notifications } from '$lib/stores/notifications';
  import { copyToClipboard } from '$lib/utils/clipboard';
  import type { SingboxQosClass } from '$lib/types';

  interface Props {
    open: boolean;
    /** Настроенные классы — для подстановки DSCP/имени в сниппеты. */
    classes: SingboxQosClass[];
    onclose: () => void;
  }
  let { open, classes, onclose }: Props = $props();

  let selectedIdx = $state(0);
  // Сброс выбора при переоткрытии / изменении списка.
  $effect(() => {
    if (!open || selectedIdx >= classes.length) selectedIdx = 0;
  });

  const selected = $derived<SingboxQosClass | null>(classes[selectedIdx] ?? null);
  // Без настроенных классов подставляем рабочий пример (DSCP 46, «Пример»):
  // копируемая команда должна оставаться валидной — никаких <плейсхолдеров>
  // в тексте под кнопкой «Копировать».
  const dscpValue = $derived(selected ? String(selected.dscp) : '46');
  const policyName = $derived.by(() => {
    if (!selected) return 'AWGM-Пример';
    const raw = selected.name.trim().replace(/"/g, '');
    return `AWGM-${raw || 'class'}`;
  });

  const psSnippet = $derived(
    `New-NetQosPolicy -Name "${policyName}" -AppPathNameMatchCondition "app.exe" -DSCPAction ${dscpValue} -NetworkProfile All`,
  );
  const regSnippet =
    'reg add "HKLM\\SYSTEM\\CurrentControlSet\\Services\\Tcpip\\QoS" /v "Do not use NLA" /t REG_SZ /d 1 /f';
  const linuxSnippet = $derived(
    `iptables -t mangle -A OUTPUT -m owner --uid-owner <uid> -j DSCP --set-dscp ${dscpValue}`,
  );

  async function copy(text: string) {
    const ok = await copyToClipboard(text);
    if (ok) notifications.success('Скопировано в буфер обмена');
    else notifications.error('Не удалось скопировать');
  }
</script>

<Modal {open} title="Как пометить трафик на ПК" size="md" {onclose}>
  <div class="body">
    {#if classes.length === 0}
      <p class="hint">
        Команды ниже — пример с DSCP 46. Добавьте класс, чтобы получить готовую
        команду под него.
      </p>
    {/if}

    {#if classes.length > 1}
      <label class="field">
        <span class="lbl">Класс</span>
        <select class="inp" bind:value={selectedIdx}>
          {#each classes as cls, idx (idx)}
            <option value={idx}>DSCP {cls.dscp} — {cls.name || 'без названия'}</option>
          {/each}
        </select>
      </label>
    {/if}

    <section class="block">
      <div class="blk-cap">Windows — политика QoS (PowerShell от администратора)</div>
      <div class="code-row">
        <pre class="code">{psSnippet}</pre>
        <IconButton size="sm" ariaLabel="Копировать команду PowerShell" title="Копировать" onclick={() => void copy(psSnippet)}>
          <Copy size={14} />
        </IconButton>
      </div>
      <p class="hint">
        Замените <code class="mono">app.exe</code> на имя исполняемого файла программы,
        чей трафик нужно пометить.
      </p>
      <p class="hint">
        В версиях Pro, Enterprise и Education настроить DSCP-метки можно через
        редактор локальной групповой политики (<code class="mono">gpedit.msc</code>)
        → Конфигурация компьютера → Конфигурация Windows → QoS на основе политики.
      </p>
    </section>

    <section class="block">
      <div class="blk-cap">Недоменные / мульти-NIC машины</div>
      <div class="code-row">
        <pre class="code">{regSnippet}</pre>
        <IconButton size="sm" ariaLabel="Копировать команду reg add" title="Копировать" onclick={() => void copy(regSnippet)}>
          <Copy size={14} />
        </IconButton>
      </div>
      <p class="hint">
        Без этого ключа Windows вне домена (или с несколькими сетевыми адаптерами)
        может игнорировать политику QoS. После добавления перезагрузите ПК.
      </p>
    </section>

    <section class="block">
      <div class="blk-cap">Заметки</div>
      <ul class="notes">
        <li>
          Linux: <code class="mono">{linuxSnippet}</code>
        </li>
        <li>macOS: пометка DSCP для приложений не поддерживается системно.</li>
        <li>Wi-Fi (WMM) может перезаписать DSCP-метку — проверяйте по кабелю.</li>
      </ul>
    </section>
  </div>
</Modal>

<style>
  .body {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .field { display: flex; flex-direction: column; gap: 4px; }
  .lbl { font-size: 11px; color: var(--text-muted); font-weight: 500; }
  .inp {
    padding: 6px 10px;
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    border: 1px solid var(--border);
    color: var(--text-primary);
    font-size: 12.5px;
    font-family: inherit;
  }
  .block { display: flex; flex-direction: column; gap: 6px; }
  .blk-cap {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .code-row {
    display: flex;
    align-items: flex-start;
    gap: 6px;
  }
  .code {
    flex: 1;
    min-width: 0;
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    font-family: var(--font-mono);
    font-size: 11px;
    line-height: 1.5;
    color: var(--text-secondary);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .hint { margin: 0; font-size: 11.5px; color: var(--text-muted); line-height: 1.4; }
  .notes {
    margin: 0;
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 11.5px;
    color: var(--text-muted);
    line-height: 1.5;
  }
  code.mono {
    font-family: var(--font-mono);
    font-size: 10.5px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 0 3px;
    color: var(--text-secondary);
    word-break: break-all;
  }
</style>
