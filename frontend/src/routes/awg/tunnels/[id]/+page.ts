import type { PageLoad } from './$types';
import { superValidate } from 'sveltekit-superforms';
import { zod4 } from 'sveltekit-superforms/adapters';
import { editTunnelSchema } from '$lib/schemas/tunnel';

export const load: PageLoad = async () => {
	const form = await superValidate(zod4(editTunnelSchema));
	return { form };
};
