import { skeleton } from '@skeletonlabs/skeleton/plugin';
import * as themes from '@skeletonlabs/skeleton/themes';
import forms from '@tailwindcss/forms';
import type { Config } from 'tailwindcss';

export default {
    content: [
        './src/**/*.{html,js,svelte,ts}',
        require.resolve('@skeletonlabs/skeleton-svelte')
    ],
    plugins: [
        forms,
        skeleton({
            themes: [themes.cerberus, themes.catppuccin]
        })
    ]
} satisfies Config;
