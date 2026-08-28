import type {
	ConfigSlotContentResponse,
	ConfigSlotsResponse,
	ConnectivityResult,
	DeviceProxyConfig,
	DeviceProxyInstance,
	DeviceProxyOutbound,
	DeviceProxyRuntime,
	IPResult,
	SingboxConfigPreview,
	SingboxImportResponse,
	SingboxInboundsList,
	SingboxStatus,
	SingboxTunnel,
	UserConfigApplyResponse,
	UserConfigCheckResponse
} from '$lib/types';
import { RoutingClient } from './clientRouting';

export class SingboxClient extends RoutingClient {
	// ─────────────────────────────────────────────
	// #region Sing-box
	// ─────────────────────────────────────────────

	async singboxGetStatus(): Promise<SingboxStatus> {
		return this.request('/singbox/status');
	}

	async singboxGetClientsByIP(): Promise<{ clientsByIP: Record<string, string> }> {
		return this.request('/singbox/connections/clients');
	}

	// Kill a single sing-box connection by Clash UUID. Bypasses request()
	// because ClashProxy returns 204 with no JSON envelope. Returns true on
	// success so callers can decide whether to roll back optimistic UI.
	async singboxKillConnection(id: string): Promise<boolean> {
		const url = `${this.baseUrl}/singbox/clash/connections/${encodeURIComponent(id)}`;
		try {
			const r = await fetch(url, {
				method: 'DELETE',
				credentials: 'same-origin',
				signal: this.abortController.signal,
			});
			return r.ok;
		} catch {
			return false;
		}
	}

	// Bulk-kill: returns counts so the caller can surface partial failure.
	async singboxKillConnections(ids: string[]): Promise<{ ok: number; total: number }> {
		const results = await Promise.all(ids.map((id) => this.singboxKillConnection(id)));
		const ok = results.filter(Boolean).length;
		return { ok, total: ids.length };
	}

	async singboxInstall(): Promise<SingboxStatus> {
		return this.request('/singbox/install', { method: 'POST' });
	}

	async singboxUpdate(): Promise<SingboxStatus> {
		return this.request('/singbox/update', { method: 'POST' });
	}

	async singboxUninstall(): Promise<SingboxStatus> {
		return this.request('/singbox/uninstall', { method: 'POST' });
	}

	async singboxControl(action: 'start' | 'stop' | 'restart'): Promise<SingboxStatus> {
		return this.request('/singbox/control', {
			method: 'POST',
			body: JSON.stringify({ action }),
		});
	}

	async singboxToggleNDMSProxy(enabled: boolean): Promise<{ enabled: boolean; migrated: boolean }> {
		return this.request('/singbox/ndms-proxy', {
			method: 'POST',
			body: JSON.stringify({ enabled }),
		});
	}



	async singboxGetConfigPreview(): Promise<SingboxConfigPreview> {
		return this.request<SingboxConfigPreview>('/singbox/config-preview');
	}

	// ── Эксперт-редактор конфигурации (config.d слоты) ──

	async singboxConfigSlots(): Promise<ConfigSlotsResponse> {
		return this.request<ConfigSlotsResponse>('/singbox/config/slots');
	}

	async singboxConfigSlot(name: string): Promise<ConfigSlotContentResponse> {
		return this.request<ConfigSlotContentResponse>(
			`/singbox/config/slot?name=${encodeURIComponent(name)}`,
		);
	}

	/** Сохранить черновик user-слота: тело — сырой JSON слота целиком. */
	async singboxUserConfigSave(content: string): Promise<void> {
		await this.request('/singbox/config/user', { method: 'PUT', body: content });
	}

	/**
	 * Проверить конфиг без записи. content опционален — без него проверяется
	 * текущий черновик (409 если черновика нет).
	 */
	async singboxUserConfigCheck(content?: string): Promise<UserConfigCheckResponse> {
		return this.request<UserConfigCheckResponse>('/singbox/config/user/check', {
			method: 'POST',
			...(content !== undefined ? { body: content } : {}),
		});
	}

	/**
	 * Применить черновик: 422 (validation/sbCheck) прилетает через
	 * err.status/err.body; 200 может нести advisory-предупреждения.
	 */
	async singboxUserConfigApply(): Promise<UserConfigApplyResponse> {
		return this.request<UserConfigApplyResponse>('/singbox/config/user/apply', {
			method: 'POST',
		});
	}

	async singboxUserConfigDiscard(): Promise<void> {
		await this.request('/singbox/config/user/discard', { method: 'POST' });
	}

	async singboxUserConfigEnable(enabled: boolean): Promise<void> {
		await this.request('/singbox/config/user/enable', {
			method: 'POST',
			body: JSON.stringify({ enabled }),
		});
	}

	/** Все inbound'ы merged-конфига sing-box с атрибуцией источника. */
	async listSingboxInbounds(): Promise<SingboxInboundsList> {
		return this.request<SingboxInboundsList>('/singbox/inbounds');
	}

	async singboxListTunnels(): Promise<SingboxTunnel[]> {
		return this.request('/singbox/tunnels');
	}

	async singboxImportLinks(links: string): Promise<SingboxImportResponse> {
		return this.request('/singbox/tunnels', {
			method: 'POST',
			body: JSON.stringify({ links })
		});
	}

	async singboxGetTunnel(tag: string): Promise<{ tag: string; outbound: unknown }> {
		const isMockDev = this.isMockDevMode();
		try {
			const raw = await this.request<unknown>(`/singbox/tunnels/get?tag=${encodeURIComponent(tag)}`);
			// Normal backend shape. Пустой outbound ({}) не считается ответом:
			// prism в dev:mock генерирует из swagger именно {tag:'proxy-01',
			// outbound:{}} — такой ответ должен провалиться в list-based
			// фолбэк ниже, который найдёт настоящий мок-туннель по tag.
			if (raw && typeof raw === 'object' && 'outbound' in raw && 'tag' in raw) {
				const obj = raw as { tag: string; outbound: unknown };
				const ob = obj.outbound;
				if (ob && typeof ob === 'object' && Object.keys(ob).length > 0) return obj;
			}
			// Prism may return a SingboxTunnel-like item directly instead of {tag,outbound}.
			if (isMockDev && raw && typeof raw === 'object' && 'tag' in raw) {
				const t = raw as SingboxTunnel;
				return { tag: t.tag, outbound: this.buildMockOutboundFromTunnel(t) };
			}
		} catch (err) {
			if (!isMockDev) throw err;
		}

		if (isMockDev) {
			const tunnels = await this.singboxListTunnels();
			const found = tunnels.find((t) => t.tag === tag) ?? tunnels[0];
			if (found) {
				return { tag: found.tag, outbound: this.buildMockOutboundFromTunnel(found) };
			}
		}
		throw new Error('Туннель не найден');
	}


	async singboxExportShareLink(
		outbound: unknown,
		label?: string,
	): Promise<{ link: string }> {
		return this.request('/singbox/tunnels/share-link', {
			method: 'POST',
			body: JSON.stringify({ outbound, label }),
		});
	}

	async singboxUpdateTunnel(tag: string, outbound: unknown): Promise<SingboxTunnel[]> {
		return this.request(`/singbox/tunnels?tag=${encodeURIComponent(tag)}`, {
			method: 'PUT',
			body: JSON.stringify({ outbound })
		});
	}

	async singboxRenameTunnel(oldTag: string, newTag: string): Promise<SingboxTunnel[]> {
		return this.request('/singbox/tunnels/rename', {
			method: 'PATCH',
			body: JSON.stringify({ oldTag, newTag })
		});
	}

	async singboxDeleteTunnel(tag: string): Promise<SingboxTunnel[]> {
		return this.fetchDelete<SingboxTunnel[]>(
			`${this.baseUrl}/singbox/tunnels?tag=${encodeURIComponent(tag)}`,
			{ method: 'DELETE' },
			tag,
		);
	}

	async singboxDelayCheck(tag: string): Promise<{ tag: string; delay: number }> {
		return this.request(`/singbox/tunnels/delay-check?tag=${encodeURIComponent(tag)}`, {
			method: 'POST',
		});
	}

	async singboxCheckConnectivity(tag: string, iface?: string): Promise<ConnectivityResult> {
		let url = `/singbox/tunnels/test/connectivity?tag=${encodeURIComponent(tag)}`;
		if (iface) url += `&iface=${encodeURIComponent(iface)}`;
		return this.request(url);
	}

	async singboxCheckIP(tag: string, serviceURL?: string, iface?: string): Promise<IPResult> {
		let url = `/singbox/tunnels/test/ip?tag=${encodeURIComponent(tag)}`;
		if (serviceURL) url += `&service=${encodeURIComponent(serviceURL)}`;
		if (iface) url += `&iface=${encodeURIComponent(iface)}`;
		return this.request(url);
	}

	singboxSpeedTestStream(
		tag: string,
		server: string,
		port: number,
		onPhase: (phase: 'download' | 'upload') => void,
		onInterval: (data: { phase: string; second: number; bandwidth: number }) => void,
		onResult: (data: { phase: string; bandwidth: number; bytes: number; duration: number }) => void,
		onDone: () => void,
		onError: (error: string) => void,
		iface?: string,
	): EventSource {
		const ifaceParam = iface ? `&iface=${encodeURIComponent(iface)}` : '';
		const url = `${this.baseUrl}/singbox/tunnels/test/speed/stream?tag=${encodeURIComponent(tag)}&server=${encodeURIComponent(server)}&port=${port}${ifaceParam}`;
		const es = new EventSource(url);
		es.addEventListener('phase', (e) => {
			try { onPhase(JSON.parse((e).data).phase); } catch { /* ignore */ }
		});
		es.addEventListener('interval', (e) => {
			try { onInterval(JSON.parse((e).data)); } catch { /* ignore */ }
		});
		es.addEventListener('result', (e) => {
			try { onResult(JSON.parse((e).data)); } catch { /* ignore */ }
		});
		es.addEventListener('done', () => { onDone(); es.close(); });
		es.addEventListener('error', (e) => {
			const msg = e instanceof MessageEvent ? String(e.data) : 'Соединение потеряно';
			onError(msg);
			es.close();
		});
		return es;
	}

	// #endregion


	// ─────────────────────────────────────────────
	// #region Device Proxy
	// ─────────────────────────────────────────────

	async getDeviceProxyConfig(): Promise<DeviceProxyConfig> {
		return this.request('/proxy/config');
	}

	async saveDeviceProxyConfig(cfg: DeviceProxyConfig): Promise<DeviceProxyConfig> {
		return this.request('/proxy/config', {
			method: 'PUT',
			body: JSON.stringify(cfg),
		});
	}

	async getDeviceProxyRuntime(): Promise<DeviceProxyRuntime> {
		return this.request('/proxy/runtime');
	}

	async listDeviceProxyOutbounds(): Promise<DeviceProxyOutbound[]> {
		return this.request('/proxy/outbounds');
	}

	async getDeviceProxyListenChoices(): Promise<{
		lanIP: string;
		bridges: { id: string; label: string; ip: string }[];
		singboxRunning: boolean;
	}> {
		return this.request('/proxy/listen-choices');
	}


	// ─────────────────────────────────────────────
	// #region Device Proxy — multi-instance
	// ─────────────────────────────────────────────

	async listDeviceProxyInstances(): Promise<DeviceProxyInstance[]> {
		return this.request<DeviceProxyInstance[]>('/proxy/instances');
	}

	async getDeviceProxyInstance(id: string): Promise<DeviceProxyInstance> {
		return this.request<DeviceProxyInstance>(`/proxy/instance?id=${encodeURIComponent(id)}`);
	}

	async saveDeviceProxyInstance(instance: DeviceProxyInstance): Promise<DeviceProxyInstance> {
		return this.request<DeviceProxyInstance>('/proxy/instance', {
			method: 'PUT',
			body: JSON.stringify(instance)
		});
	}

	async deleteDeviceProxyInstance(id: string): Promise<{ deleted: boolean; applied: boolean }> {
		return this.request<{ deleted: boolean; applied: boolean }>(`/proxy/instance?id=${encodeURIComponent(id)}`, {
			method: 'DELETE'
		});
	}

	async getDeviceProxyInstanceRuntime(id: string): Promise<DeviceProxyRuntime> {
		return this.request<DeviceProxyRuntime>(`/proxy/instance/runtime?id=${encodeURIComponent(id)}`);
	}

	// #endregion

	// #endregion


}
