<script lang="ts">
    // Кнопка «Обновить» разделов маршрутизации: заставляет роутер перечитать
    // конфигурацию и сразу инвалидирует секционные сторы. Переехала с шапки
    // контейнера /routing на каждую из его бывших вкладок.
    import { api } from '$lib/api/client';
    import { Button } from '$lib/components/ui';
    import { notifications } from '$lib/stores/notifications';
    import { routing, invalidateAllRouting } from '$lib/stores/routing';

    let refreshing = $state(false);
    let missing = $derived($routing.missing);

    async function handleRefresh() {
        if (refreshing) return;
        refreshing = true;
        try {
            const res = await api.refreshRouting();
            // Force every section store to refetch now (the backend also
            // posts resource:invalidated hints, but a local kick keeps the
            // UI responsive even if SSE happens to be lagging).
            invalidateAllRouting();
            if (res.missing.length === 0) {
                notifications.success('Данные получены');
            } else {
                notifications.warning(`Не удалось загрузить: ${res.missing.join(', ')}`);
            }
        } catch (e) {
            notifications.error(`Ошибка обновления: ${(e as Error).message}`);
        } finally {
            refreshing = false;
        }
    }
</script>

<Button
    variant="secondary"
    size="sm"
    onclick={handleRefresh}
    disabled={refreshing}
    loading={refreshing}
>
    {#if missing.length > 0}
        Загрузить недостающее ({missing.length})
    {:else}
        Обновить
    {/if}
</Button>
