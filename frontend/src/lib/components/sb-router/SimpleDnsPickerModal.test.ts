import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('$lib/api/client', () => ({
  api: { singboxRouterUpdateDNSServer: vi.fn(async () => {}) },
}));

vi.mock('$lib/stores/singboxRouter', () => ({
  singboxRouter: { loadAll: vi.fn(async () => {}) },
}));

import { render, screen, fireEvent } from '@testing-library/svelte';
import { api } from '$lib/api/client';
import SimpleDnsPickerModal from './SimpleDnsPickerModal.svelte';

const direct = { tag: 'dns-direct', type: 'udp' as const, server: '77.88.8.8' };

describe('SimpleDnsPickerModal', () => {
  beforeEach(() => vi.clearAllMocks());

  it('текущий адрес отмечен как выбранный пресет', () => {
    render(SimpleDnsPickerModal, {
      props: { server: direct, allowProtocol: true, onclose: vi.fn(), onsaved: vi.fn() },
    });
    const yandex = screen.getByRole('radio', { name: /Яндекс/ }) as HTMLInputElement;
    expect(yandex.checked).toBe(true);
  });

  it('фильтрующие провайдеры подписаны в списке', () => {
    render(SimpleDnsPickerModal, {
      props: { server: direct, allowProtocol: true, onclose: vi.fn(), onsaved: vi.fn() },
    });
    expect(screen.getByText('блокирует malware')).toBeTruthy();
    expect(screen.getByText('блокирует рекламу')).toBeTruthy();
  });

  it('выбор Cloudflare + DoH сохраняет корректный DTO и зовёт onsaved', async () => {
    const onsaved = vi.fn();
    render(SimpleDnsPickerModal, {
      props: { server: direct, allowProtocol: true, onclose: vi.fn(), onsaved },
    });
    await fireEvent.click(screen.getByRole('radio', { name: /Cloudflare/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'DoH' }));
    await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));
    expect(api.singboxRouterUpdateDNSServer).toHaveBeenCalledWith('dns-direct', {
      tag: 'dns-direct',
      type: 'https',
      server: '1.1.1.1',
      server_port: 443,
      path: '/dns-query',
      tls: { server_name: 'cloudflare-dns.com' },
    });
    expect(onsaved).toHaveBeenCalled();
  });

  it('туннельный сервер: переключателя протокола нет, detour сохраняется', async () => {
    const tunnel = { tag: 'dns-tunnel', type: 'udp' as const, server: '9.9.9.9', detour: 'wg-nl' };
    render(SimpleDnsPickerModal, {
      props: { server: tunnel, allowProtocol: false, onclose: vi.fn(), onsaved: vi.fn() },
    });
    expect(screen.queryByRole('button', { name: 'DoH' })).toBeNull();
    await fireEvent.click(screen.getByRole('radio', { name: /Google/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));
    expect(api.singboxRouterUpdateDNSServer).toHaveBeenCalledWith('dns-tunnel', {
      tag: 'dns-tunnel',
      type: 'udp',
      server: '8.8.8.8',
      detour: 'wg-nl',
    });
  });

  it('свой адрес: протокол прижат к UDP', async () => {
    render(SimpleDnsPickerModal, {
      props: { server: direct, allowProtocol: true, onclose: vi.fn(), onsaved: vi.fn() },
    });
    await fireEvent.click(screen.getByRole('radio', { name: /Свой адрес/ }));
    await fireEvent.input(screen.getByPlaceholderText('192.168.1.1'), {
      target: { value: '192.168.1.1' },
    });
    expect(screen.getByText('Свой адрес доступен только по обычному DNS')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));
    expect(api.singboxRouterUpdateDNSServer).toHaveBeenCalledWith('dns-direct', {
      tag: 'dns-direct',
      type: 'udp',
      server: '192.168.1.1',
    });
  });

  it('пустой свой адрес — сохранение заблокировано', async () => {
    render(SimpleDnsPickerModal, {
      props: { server: direct, allowProtocol: true, onclose: vi.fn(), onsaved: vi.fn() },
    });
    await fireEvent.click(screen.getByRole('radio', { name: /Свой адрес/ }));
    expect((screen.getByRole('button', { name: 'Сохранить' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('переход на UDP с настроенными пинами предупреждает о потере', async () => {
    const pinned = {
      tag: 'dns-direct',
      type: 'https' as const,
      server: '1.1.1.1',
      server_port: 443,
      path: '/dns-query',
      tls: { server_name: 'cloudflare-dns.com', certificate_public_key_sha256: ['AAAA'] },
    };
    render(SimpleDnsPickerModal, {
      props: { server: pinned, allowProtocol: true, onclose: vi.fn(), onsaved: vi.fn() },
    });
    await fireEvent.click(screen.getByRole('button', { name: 'UDP' }));
    expect(screen.getByText(/Настройки TLS будут удалены/)).toBeTruthy();
  });

  it('ошибка API показывается, модалка не закрывается', async () => {
    (api.singboxRouterUpdateDNSServer as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error('boom'));
    const onclose = vi.fn();
    const onsaved = vi.fn();
    render(SimpleDnsPickerModal, {
      props: { server: direct, allowProtocol: true, onclose, onsaved },
    });
    await fireEvent.click(screen.getByRole('radio', { name: /Google/ }));
    await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));
    expect(await screen.findByText('boom')).toBeTruthy();
    expect(onsaved).not.toHaveBeenCalled();
    expect(onclose).not.toHaveBeenCalled();
  });
});
