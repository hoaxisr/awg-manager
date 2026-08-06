<script lang="ts">
    import type { Snippet } from 'svelte';
    import { BackLink } from '$lib/components/ui';

    interface Props {
        title: string;
        description?: string;
        actions?: Snippet;
        /**
         * Пилюли/бейджи в одну строку с заголовком (состояние и режим движка).
         * Отдельный сниппет, а не часть `actions`: это утверждение о странице,
         * а не действие, и на узком экране оно обязано остаться у заголовка,
         * тогда как actions уезжают на свою строку.
         */
        badges?: Snippet;
        backTo?: string;
    }

    let { title, description, actions, badges, backTo }: Props = $props();
</script>

<div class="page-header">
    <div class="header-left">
        {#if backTo}
            <BackLink href={backTo} />
        {/if}
        <div>
            <div class="title-row">
                <h1>{title}</h1>
                {#if badges}
                    {@render badges()}
                {/if}
            </div>
            {#if description}
                <p class="description">{description}</p>
            {/if}
        </div>
    </div>

    {#if actions}
        <div class="actions">
            {@render actions()}
        </div>
    {/if}
</div>

<style>
    .page-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 1rem;
        margin-bottom: 1.5rem;
    }

    .header-left {
        display: flex;
        align-items: center;
        gap: 1rem;
        min-width: 0;
    }

    /* Бейджи переносятся под заголовок, а не растягивают строку: заголовки
       страниц бывают из одного длинного слова, и flex-строка без переноса
       уехала бы за вьюпорт (body скрывает overflow-x). */
    .title-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 0.5rem;
        min-width: 0;
    }

    .page-header h1 {
        font-size: 1.5rem;
        font-weight: 600;
        color: var(--text-primary);
        margin: 0;
    }

    .description {
        color: var(--text-muted);
        font-size: 0.875rem;
        margin: 0.25rem 0 0;
    }

    .actions {
        display: flex;
        gap: 0.5rem;
        flex-shrink: 0;
    }

    /* On mobile, stack title above actions: long page titles (single
       unbreakable words like "Маршрутизация") + `flex-shrink: 0` on
       .actions otherwise force the row to overflow past the viewport.
       Body has `overflow-x: hidden` (app.css), so the overflow is
       silently clipped AND a `ghost`-variant Button (transparent
       background) ends up visually under the title text — user
       perceives the button as missing. Stacking vertically gives the
       actions their own row with full width, no overlap. Breakpoint
       mirrors the `.setting-row` / `.section-header` patterns already
       in app.css. */
    @media (max-width: 768px) {
        .page-header {
            flex-direction: column;
            align-items: stretch;
            gap: 0.75rem;
        }
        .actions {
            align-self: stretch;
            width: 100%;
            min-width: 0;
            flex-wrap: wrap;
            justify-content: flex-end;
        }
    }
</style>
