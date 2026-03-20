import { skeleton } from '@skeletonlabs/skeleton/plugin';
import * as themes from '@skeletonlabs/skeleton/themes';
import forms from '@tailwindcss/forms';
import type { Config } from 'tailwindcss';

export default {
    content: [
        './src/**/*.{html,js,svelte,ts}',
        require.resolve('@skeletonlabs/skeleton-svelte')
    ],
    theme: {
        extend: {
            colors: {
                // Tokyo Night theme colors for custom use
                'tokyo': {
                    'bg': '#1a1b26',
                    'bg-dark': '#16161e',
                    'fg': '#a9b1d6',
                    'blue': '#7aa2f7',
                    'cyan': '#7dcfff',
                    'green': '#9ece6a',
                    'red': '#f7768e',
                    'yellow': '#e0af68',
                    'purple': '#bb9af7',
                }
            }
        }
    },
    plugins: [
        forms,
        skeleton({
            themes: [themes.cerberus, themes.catppuccin]
        })
    ]
} satisfies Config;
