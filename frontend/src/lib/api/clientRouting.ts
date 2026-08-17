import type {
	AccessPolicy,
	ClientRoute,
	DnsRoute,
	PolicyDevice,
	ResolveResult,
	StaticRouteList
} from '$lib/types';
import { ServersClient } from './clientServers';

export class RoutingClient extends ServersClient {
	// ─────────────────────────────────────────────
	// #region Static IP Routes
	// ─────────────────────────────────────────────

	async createStaticRoute(rl: Partial<StaticRouteList>): Promise<StaticRouteList> {
		return this.request('/static-routes/create', {
			method: 'POST',
			body: JSON.stringify(rl)
		});
	}

	async updateStaticRoute(rl: StaticRouteList): Promise<StaticRouteList> {
		return this.request('/static-routes/update', {
			method: 'POST',
			body: JSON.stringify(rl)
		});
	}

	async deleteStaticRoute(id: string): Promise<void> {
		return this.request(`/static-routes/delete?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async setStaticRouteEnabled(id: string, enabled: boolean): Promise<void> {
		return this.request(`/static-routes/set-enabled?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	async importStaticRoutes(tunnelID: string, name: string, content: string): Promise<StaticRouteList> {
		return this.request('/static-routes/import', {
			method: 'POST',
			body: JSON.stringify({ tunnelID, name, content })
		});
	}

	// #endregion


	// ─────────────────────────────────────────────
	// #region Routing — resolve, tunnels
	// ─────────────────────────────────────────────

	async resolveDomain(domain: string): Promise<ResolveResult> {
		return this.request(`/routing/resolve?domain=${encodeURIComponent(domain)}`);
	}

	async refreshRouting(): Promise<{ missing: string[] }> {
		return this.request('/routing/refresh', { method: 'POST' });
	}

	// #endregion


	// ─────────────────────────────────────────────
	// #region DNS Routes — CRUD, batch, subscriptions
	// ─────────────────────────────────────────────

	async getDnsRoute(id: string): Promise<DnsRoute> {
		return this.request(`/dns-routes/get?id=${encodeURIComponent(id)}`);
	}

	async createDnsRoute(route: Partial<DnsRoute>): Promise<DnsRoute> {
		return this.request('/dns-routes/create', {
			method: 'POST',
			body: JSON.stringify(route)
		});
	}

	async updateDnsRoute(id: string, route: Partial<DnsRoute>): Promise<DnsRoute> {
		return this.request(`/dns-routes/update?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify(route)
		});
	}

	async deleteDnsRoute(id: string): Promise<DnsRoute[]> {
		return this.request(`/dns-routes/delete?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async setDnsRouteEnabled(id: string, enabled: boolean): Promise<DnsRoute[]> {
		return this.request(`/dns-routes/set-enabled?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	async createDnsRouteBatch(lists: Array<Partial<DnsRoute>>): Promise<{ created: number; lists: DnsRoute[] }> {
		return this.request('/dns-routes/create-batch', {
			method: 'POST',
			body: JSON.stringify(lists)
		});
	}

	async deleteDnsRouteBatch(ids: string[]): Promise<DnsRoute[]> {
		return this.request('/dns-routes/delete-batch', {
			method: 'POST',
			body: JSON.stringify({ ids })
		});
	}

	async refreshDnsRouteSubscriptions(id?: string): Promise<DnsRoute[]> {
		const endpoint = id
			? `/dns-routes/refresh?id=${encodeURIComponent(id)}`
			: '/dns-routes/refresh';
		return this.request(endpoint, { method: 'POST' });
	}

	async bulkDnsRouteBackend(listIDs: string[], backend: 'ndms' | 'hydraroute'): Promise<DnsRoute[]> {
		return this.request('/dns-routes/bulk-backend', {
			method: 'POST',
			body: JSON.stringify({ listIDs, backend }),
		});
	}

	// #endregion


	// ─────────────────────────────────────────────
	// #region Access Policies — CRUD, devices, interfaces
	// ─────────────────────────────────────────────

	/**
	 * Состав политик целиком: отдельной ручки «в какой политике этот интерфейс»
	 * у бэкенда нет, обратное членство ищется по этому списку.
	 */
	async listAccessPolicies(): Promise<AccessPolicy[]> {
		return this.request<AccessPolicy[]>('/access-policies');
	}

	async createAccessPolicy(description: string): Promise<AccessPolicy> {
		return this.request('/access-policies/create', {
			method: 'POST',
			body: JSON.stringify({ description }),
		});
	}

	async deleteAccessPolicy(name: string): Promise<void> {
		return this.request(`/access-policies/delete?name=${encodeURIComponent(name)}`, {
			method: 'DELETE',
		});
	}

	async setAccessPolicyDescription(name: string, description: string): Promise<void> {
		return this.request('/access-policies/description', {
			method: 'POST',
			body: JSON.stringify({ name, description }),
		});
	}

	async setAccessPolicyStandalone(name: string, enabled: boolean): Promise<void> {
		return this.request('/access-policies/standalone', {
			method: 'POST',
			body: JSON.stringify({ name, enabled }),
		});
	}

	async permitPolicyInterface(name: string, iface: string, order: number): Promise<void> {
		return this.request('/access-policies/permit', {
			method: 'POST',
			body: JSON.stringify({ name, interface: iface, order }),
		});
	}

	async denyPolicyInterface(name: string, iface: string): Promise<void> {
		return this.request(`/access-policies/permit?name=${encodeURIComponent(name)}&interface=${encodeURIComponent(iface)}`, {
			method: 'DELETE',
		});
	}

	async assignDeviceToPolicy(mac: string, policy: string): Promise<void> {
		return this.request('/access-policies/assign', {
			method: 'POST',
			body: JSON.stringify({ mac, policy }),
		});
	}

	async unassignDeviceFromPolicy(mac: string): Promise<void> {
		return this.request(`/access-policies/assign?mac=${encodeURIComponent(mac)}`, {
			method: 'DELETE',
		});
	}

	async listPolicyDevices(): Promise<PolicyDevice[]> {
		return this.request<PolicyDevice[]>('/routing/policy-devices');
	}

	async setPolicyInterfaceUp(name: string, up: boolean): Promise<void> {
		return this.request('/access-policies/interface-up', {
			method: 'POST',
			body: JSON.stringify({ name, up }),
		});
	}

	// #endregion


	// ─────────────────────────────────────────────
	// #region Client Routes
	// ─────────────────────────────────────────────

	async createClientRoute(data: Partial<ClientRoute>): Promise<ClientRoute> {
		return this.request('/client-routes/create', {
			method: 'POST',
			body: JSON.stringify(data)
		});
	}

	async updateClientRoute(id: string, data: Partial<ClientRoute>): Promise<ClientRoute> {
		return this.request(`/client-routes/update?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify(data)
		});
	}

	async deleteClientRoute(id: string): Promise<void> {
		return this.request(`/client-routes/delete?id=${encodeURIComponent(id)}`, {
			method: 'POST'
		});
	}

	async toggleClientRoute(id: string, enabled: boolean): Promise<void> {
		return this.request(`/client-routes/toggle?id=${encodeURIComponent(id)}`, {
			method: 'POST',
			body: JSON.stringify({ enabled })
		});
	}

	// #endregion


}
