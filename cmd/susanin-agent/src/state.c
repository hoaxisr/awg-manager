#include "state.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void set_init(state_set *st)
{
    st->v = NULL;
    st->n = 0;
    st->cap = 0;
}

void state_init(susanin_state *s)
{
    set_init(&s->test_tcp); set_init(&s->ok_tcp);
    set_init(&s->watch_tcp); set_init(&s->cooldown_tcp);
    set_init(&s->test_udp); set_init(&s->ok_udp);
    set_init(&s->watch_udp); set_init(&s->cooldown_udp);
}

static void set_free(state_set *st)
{
    free(st->v);
    st->v = NULL; st->n = 0; st->cap = 0;
}

void state_free(susanin_state *s)
{
    set_free(&s->test_tcp); set_free(&s->ok_tcp);
    set_free(&s->watch_tcp); set_free(&s->cooldown_tcp);
    set_free(&s->test_udp); set_free(&s->ok_udp);
    set_free(&s->watch_udp); set_free(&s->cooldown_udp);
}

void state_expire(state_set *st, time_t now)
{
    int i, w = 0;
    for (i = 0; i < st->n; i++) {
        if (st->v[i].expire > now)
            st->v[w++] = st->v[i];
    }
    st->n = w;
}

int state_has(const state_set *st, const char *addr, time_t now)
{
    int i;
    for (i = 0; i < st->n; i++) {
        if (st->v[i].expire > now && strcmp(st->v[i].addr, addr) == 0)
            return 1;
    }
    return 0;
}

time_t state_at(const state_set *st, const char *addr, time_t now)
{
    int i;
    for (i = 0; i < st->n; i++) {
        if (st->v[i].expire > now && strcmp(st->v[i].addr, addr) == 0)
            return st->v[i].expire;
    }
    return 0;
}

static int set_reserve(state_set *st, int need)
{
    if (st->n + need <= st->cap)
        return 0;
    {
        int ncap = st->cap ? st->cap * 2 : 16;
        state_entry *nv = realloc(st->v, (size_t)ncap * sizeof(state_entry));
        if (!nv)
            return -1;
        st->v = nv;
        st->cap = ncap;
    }
    return 0;
}

int state_add(state_set *st, const char *addr, time_t now, int ttl, int refresh)
{
    int i;
    state_entry *e;
    time_t expire;
    if (ttl <= 0)
        expire = (time_t)0x7fffffff;   /* ttl 0 = never expire */
    else
        expire = now + ttl;
    if (!refresh) {
        if (state_has(st, addr, now))
            state_remove(st, addr);
    } else {
        for (i = 0; i < st->n; i++) {
            if (st->v[i].expire > now && strcmp(st->v[i].addr, addr) == 0) {
                st->v[i].expire = expire;
                return 0;
            }
        }
        return -1;
    }
    if (set_reserve(st, 1) != 0)
        return -1;
    e = &st->v[st->n++];
    snprintf(e->addr, sizeof(e->addr), "%s", addr);
    e->expire = expire;
    return 0;
}

int state_remove(state_set *st, const char *addr)
{
    int i, w = 0;
    for (i = 0; i < st->n; i++) {
        if (strcmp(st->v[i].addr, addr) != 0)
            st->v[w++] = st->v[i];
    }
    st->n = w;
    return 0;
}

static void set_save(FILE *fp, const char *name, const state_set *st)
{
    int i;
    for (i = 0; i < st->n; i++) {
        if (st->v[i].expire > 0)
            fprintf(fp, "%s %s %lld\n", name, st->v[i].addr,
                    (long long)st->v[i].expire);
    }
}

int state_save(const char *path, const susanin_state *s)
{
    FILE *fp;
    char tmp[512];
    if (!path || !s)
        return -1;
    snprintf(tmp, sizeof(tmp), "%s.tmp", path);
    fp = fopen(tmp, "w");
    if (!fp)
        return -1;
    set_save(fp, "test tcp", &s->test_tcp);
    set_save(fp, "test udp", &s->test_udp);
    set_save(fp, "ok tcp", &s->ok_tcp);
    set_save(fp, "ok udp", &s->ok_udp);
    set_save(fp, "cool tcp", &s->cooldown_tcp);
    set_save(fp, "cool udp", &s->cooldown_udp);
    fclose(fp);
    rename(tmp, path);
    return 0;
}

int state_load(const char *path, susanin_state *s)
{
    FILE *fp;
    char phase[8], proto[8], addr[64];
    long long expire;
    time_t now = time(NULL);
    if (!path || !s)
        return -1;
    fp = fopen(path, "r");
    if (!fp)
        return -1;
    while (fscanf(fp, "%7s %7s %63s %lld", phase, proto, addr, &expire) == 4) {
        state_set *set = NULL;
        int ttl;
        if (expire <= (long long)now)
            continue;
        ttl = (int)(expire - (long long)now);
        if (ttl <= 0)
            continue;
        if (!strcmp(phase, "test"))
            set = !strcmp(proto, "tcp") ? &s->test_tcp : &s->test_udp;
        else if (!strcmp(phase, "ok"))
            set = !strcmp(proto, "tcp") ? &s->ok_tcp : &s->ok_udp;
        else if (!strcmp(phase, "cool"))
            set = !strcmp(proto, "tcp") ? &s->cooldown_tcp : &s->cooldown_udp;
        if (set)
            state_add(set, addr, now, ttl, 0);
    }
    fclose(fp);
    return 0;
}
