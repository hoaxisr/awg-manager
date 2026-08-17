import type {
	CreateSubscriptionGroupInput,
	CreateSubscriptionInput,
	Subscription,
	SubscriptionActiveNowResponse,
	SubscriptionGroup,
	SubscriptionHeader,
	SubscriptionPreviewMember,
	SubscriptionRefreshResult,
	UpdateSubscriptionGroupInput,
	UpdateSubscriptionInput
} from '$lib/types';
import { SbRouterClient } from './clientSbRouter';

export class SubscriptionsClient extends SbRouterClient {

	// #region Subscriptions

	async listSubscriptions(): Promise<Subscription[]> {
		const subs = await this.request<Subscription[]>('/singbox/subscriptions');
		return this.isMockDevMode() ? subs.map((s) => this.ensureMockSubscriptionMembers(s)) : subs;
	}

	async createSubscription(in_: CreateSubscriptionInput): Promise<Subscription> {
		return this.request<Subscription>('/singbox/subscriptions/create', {
			method: 'POST',
			body: JSON.stringify(in_),
		});
	}

	async getSubscription(id: string): Promise<Subscription> {
		const sub = await this.request<Subscription>(
			`/singbox/subscriptions/get?id=${encodeURIComponent(id)}`,
		);
		return this.ensureMockSubscriptionMembers(sub);
	}

	async updateSubscription(
		id: string,
		patch: UpdateSubscriptionInput,
	): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/update?id=${encodeURIComponent(id)}`,
			{
				method: 'PUT',
				body: JSON.stringify(patch),
			},
		);
	}

	async deleteSubscription(id: string): Promise<void> {
		await this.fetchDelete<{ ok: boolean }>(
			`${this.baseUrl}/singbox/subscriptions/delete?id=${encodeURIComponent(id)}`,
			{ method: 'DELETE' },
			id,
		);
	}

	async refreshSubscription(id: string): Promise<SubscriptionRefreshResult> {
		return this.request<SubscriptionRefreshResult>(
			`/singbox/subscriptions/refresh?id=${encodeURIComponent(id)}`,
			{ method: 'POST' },
		);
	}

	async getSubscriptionActiveNow(id: string): Promise<SubscriptionActiveNowResponse> {
		return this.request<SubscriptionActiveNowResponse>(
			`/singbox/subscriptions/active-now?id=${encodeURIComponent(id)}`,
		);
	}

	async setSubscriptionActiveMember(id: string, memberTag: string): Promise<void> {
		await this.request(
			`/singbox/subscriptions/active-member?id=${encodeURIComponent(id)}`,
			{
				method: 'POST',
				body: JSON.stringify({ memberTag }),
			},
		);
	}

	async moveSubscriptionRejectedToInfo(id: string, memberTag: string): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/rejected/to-info?id=${encodeURIComponent(id)}`,
			{ method: 'POST', body: JSON.stringify({ memberTag }) },
		);
	}

	async removeSubscriptionInfoItem(id: string, itemId: string): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/info/remove?id=${encodeURIComponent(id)}`,
			{ method: 'POST', body: JSON.stringify({ itemId }) },
		);
	}

	async deleteSubscriptionOrphans(id: string): Promise<void> {
		await this.request(
			`/singbox/subscriptions/orphans/delete?id=${encodeURIComponent(id)}`,
			{ method: 'POST' },
		);
	}

	async addSubscriptionMember(id: string, shareLink: string): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/members/add?id=${encodeURIComponent(id)}`,
			{
				method: 'POST',
				body: JSON.stringify({ shareLink }),
			},
		);
	}

	/**
	 * Remove one member from an inline subscription. Returns the updated
	 * subscription, or null when removing the last member tore down the
	 * whole subscription (the caller should navigate away in that case).
	 */
	async removeSubscriptionMember(id: string, memberTag: string): Promise<Subscription | null> {
		const data = await this.request<{ deleted: boolean; subscription?: Subscription }>(
			`/singbox/subscriptions/members/remove?id=${encodeURIComponent(id)}`,
			{
				method: 'POST',
				body: JSON.stringify({ memberTag }),
			},
		);
		return data.deleted ? null : (data.subscription ?? null);
	}

	async excludeSubscriptionMembers(id: string, memberTags: string[]): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/members/exclude?id=${encodeURIComponent(id)}`,
			{ method: 'POST', body: JSON.stringify({ memberTags }) },
		);
	}

	async restoreSubscriptionMembers(id: string, memberTags: string[]): Promise<Subscription> {
		return this.request<Subscription>(
			`/singbox/subscriptions/members/restore?id=${encodeURIComponent(id)}`,
			{ method: 'POST', body: JSON.stringify({ memberTags }) },
		);
	}

	async previewSubscription(input: {
		url: string;
		headers: SubscriptionHeader[];
	}): Promise<SubscriptionPreviewMember[]> {
		return this.request<SubscriptionPreviewMember[]>('/singbox/subscriptions/preview', {
			method: 'POST',
			body: JSON.stringify(input),
		});
	}

	async detectSubscriptionHeaders(url: string): Promise<{
		kind: string;
		decryptedUrl?: string;
		isEncrypted?: boolean;
		headers: SubscriptionHeader[];
		headersText: string;
		label: string;
		serverCount: number;
	}> {
		return this.request<{
			kind: string;
			decryptedUrl?: string;
			isEncrypted?: boolean;
			headers: SubscriptionHeader[];
			headersText: string;
			label: string;
			serverCount: number;
		}>('/singbox/subscriptions/detect-headers', {
			method: 'POST',
			body: JSON.stringify({ url }),
		});
	}

	// Сводные группы подписок (#372)

	async listSubscriptionGroups(): Promise<SubscriptionGroup[]> {
		return this.request<SubscriptionGroup[]>('/singbox/subscriptions/groups');
	}

	async createSubscriptionGroup(in_: CreateSubscriptionGroupInput): Promise<SubscriptionGroup> {
		return this.request<SubscriptionGroup>('/singbox/subscriptions/groups/create', {
			method: 'POST',
			body: JSON.stringify(in_),
		});
	}

	async updateSubscriptionGroup(
		id: string,
		patch: UpdateSubscriptionGroupInput,
	): Promise<SubscriptionGroup> {
		return this.request<SubscriptionGroup>(
			`/singbox/subscriptions/groups/update?id=${encodeURIComponent(id)}`,
			{
				method: 'PUT',
				body: JSON.stringify(patch),
			},
		);
	}

	async deleteSubscriptionGroup(id: string): Promise<void> {
		await this.request(`/singbox/subscriptions/groups/delete`, {
			method: 'POST',
			body: JSON.stringify({ id }),
		});
	}

	// #endregion


}
