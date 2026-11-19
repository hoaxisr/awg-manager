/* Dump format must match the differential oracle byte for byte. */
#include <ctype.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/dnswire.h"

static int hexval(char c)
{
    if (c >= '0' && c <= '9') {
        return c - '0';
    }
    if (c >= 'a' && c <= 'f') {
        return c - 'a' + 10;
    }
    if (c >= 'A' && c <= 'F') {
        return c - 'A' + 10;
    }
    return -1;
}

static int hex_decode(const char *s, uint8_t *out, size_t out_cap,
                      size_t *out_len)
{
    size_t n = strlen(s);
    if (n % 2 != 0) {
        return -1;
    }
    size_t w = 0;
    for (size_t i = 0; i < n; i += 2) {
        int hi = hexval(s[i]);
        int lo = hexval(s[i + 1]);
        if (hi < 0 || lo < 0) {
            return -1;
        }
        if (w >= out_cap) {
            return -1;
        }
        out[w++] = (uint8_t)((hi << 4) | lo);
    }
    *out_len = w;
    return 0;
}

static const char *type_name(uint16_t t, char *buf, size_t buf_len)
{
    switch (t) {
    case FIRC_DNS_TYPE_A:     return "A";
    case FIRC_DNS_TYPE_NS:    return "NS";
    case FIRC_DNS_TYPE_CNAME: return "CNAME";
    case FIRC_DNS_TYPE_SOA:   return "SOA";
    case FIRC_DNS_TYPE_PTR:   return "PTR";
    case FIRC_DNS_TYPE_MX:    return "MX";
    case FIRC_DNS_TYPE_AAAA:  return "AAAA";
    case FIRC_DNS_TYPE_SRV:   return "SRV";
    case FIRC_DNS_TYPE_OPT:   return "OPT";
    default:
        snprintf(buf, buf_len, "TYPE%u", t);
        return buf;
    }
}

static void lower_str(char *s)
{
    for (; *s != '\0'; s++) {
        *s = (char)tolower((unsigned char)*s);
    }
}

static void name_lower(const uint8_t *name, size_t name_len, char *buf,
                       size_t buf_len)
{
    if (firc_dns_name_to_string(name, name_len, buf, buf_len, NULL) != FIRC_OK) {
        snprintf(buf, buf_len, "NAME?");
        return;
    }
    lower_str(buf);
}

static void rr_rdata(const firc_dns_rr_t *rr, char *buf, size_t buf_len)
{
    char name[1024];
    switch (rr->rtype) {
    case FIRC_DNS_TYPE_A:
        if (rr->rdata_len == 4) {
            snprintf(buf, buf_len, "A=%u.%u.%u.%u", rr->rdata[0],
                     rr->rdata[1], rr->rdata[2], rr->rdata[3]);
        } else {
            snprintf(buf, buf_len, "A?");
        }
        break;
    case FIRC_DNS_TYPE_AAAA:
        if (rr->rdata_len == 16) {
            char tmp[64];
            int w = 0;
            uint16_t g[8];
            for (int i = 0; i < 8; i++) {
                size_t off = (size_t)i * 2;
                g[i] = (uint16_t)((rr->rdata[off] << 8) | rr->rdata[off + 1]);
            }
            int best_start = -1, best_len = 0, cur_start = -1, cur_len = 0;
            for (int i = 0; i < 8; i++) {
                if (g[i] == 0) {
                    if (cur_start < 0) {
                        cur_start = i;
                        cur_len = 1;
                    } else {
                        cur_len++;
                    }
                    if (cur_len > best_len) {
                        best_len = cur_len;
                        best_start = cur_start;
                    }
                } else {
                    cur_start = -1;
                    cur_len = 0;
                }
            }
            if (best_len < 2) {
                best_start = -1;
            }
            for (int i = 0; i < 8;) {
                if (i == best_start) {
                    w += snprintf(tmp + w, sizeof(tmp) - (size_t)w, ":");
                    if (i == 0) {
                        w += snprintf(tmp + w, sizeof(tmp) - (size_t)w, ":");
                    }
                    i += best_len;
                    continue;
                }
                w += snprintf(tmp + w, sizeof(tmp) - (size_t)w, "%x", g[i]);
                i++;
                if (i < 8 && i != best_start) {
                    w += snprintf(tmp + w, sizeof(tmp) - (size_t)w, ":");
                }
            }
            snprintf(buf, buf_len, "AAAA=%s", tmp);
        } else {
            snprintf(buf, buf_len, "AAAA?");
        }
        break;
    case FIRC_DNS_TYPE_CNAME:
        name_lower(rr->rdata, rr->rdata_len, name, sizeof(name));
        snprintf(buf, buf_len, "CNAME=%s", name);
        break;
    case FIRC_DNS_TYPE_NS:
        name_lower(rr->rdata, rr->rdata_len, name, sizeof(name));
        snprintf(buf, buf_len, "NS=%s", name);
        break;
    case FIRC_DNS_TYPE_PTR:
        name_lower(rr->rdata, rr->rdata_len, name, sizeof(name));
        snprintf(buf, buf_len, "PTR=%s", name);
        break;
    case FIRC_DNS_TYPE_MX: {
        /* canonical rdata: 2-byte preference + name */
        if (rr->rdata_len < 3) {
            snprintf(buf, buf_len, "MX?");
            break;
        }
        uint16_t pref = (uint16_t)((rr->rdata[0] << 8) | rr->rdata[1]);
        name_lower(rr->rdata + 2, rr->rdata_len - 2, name, sizeof(name));
        snprintf(buf, buf_len, "MX=%u,%s", pref, name);
        break;
    }
    case FIRC_DNS_TYPE_SOA: {
        /* canonical rdata: mname + rname + 20 bytes of counters */
        char mname[1024], rname[1024];
        size_t p = 0;
        size_t l1 = 0;
        while (p < rr->rdata_len && rr->rdata[p] != 0) {
            p += 1u + rr->rdata[p];
        }
        p += 1;
        l1 = p;
        name_lower(rr->rdata, l1, mname, sizeof(mname));
        size_t p2 = p;
        while (p2 < rr->rdata_len && rr->rdata[p2] != 0) {
            p2 += 1u + rr->rdata[p2];
        }
        p2 += 1;
        name_lower(rr->rdata + l1, p2 - l1, rname, sizeof(rname));
        uint32_t serial = 0;
        if (p2 + 4 <= rr->rdata_len) {
            serial = (uint32_t)rr->rdata[p2] << 24 |
                     (uint32_t)rr->rdata[p2 + 1] << 16 |
                     (uint32_t)rr->rdata[p2 + 2] << 8 |
                     (uint32_t)rr->rdata[p2 + 3];
        }
        snprintf(buf, buf_len, "SOA=%s,%s,%u", mname, rname, serial);
        break;
    }
    case FIRC_DNS_TYPE_OPT:
        snprintf(buf, buf_len, "OPT");
        break;
    default:
        snprintf(buf, buf_len, "GENERIC");
        break;
    }
}

static int cmp_str(const void *a, const void *b)
{
    return strcmp(*(const char *const *)a, *(const char *const *)b);
}

static void dump_section(const char *tag, const firc_dns_rr_t *rrs, size_t n,
                         bool sorted)
{
    char **lines = calloc(n > 0 ? n : 1, sizeof(char *));
    if (lines == NULL) { abort(); } /* truncated output would read as a false diff */
    size_t count = 0;
    for (size_t i = 0; i < n; i++) {
        char name[1024], rdata[2200], tbuf[16];
        name_lower(rrs[i].name, rrs[i].name_len, name, sizeof(name));
        rr_rdata(&rrs[i], rdata, sizeof(rdata));
        const char *tn = type_name(rrs[i].rtype, tbuf, sizeof(tbuf));
        char line[3600];
        snprintf(line, sizeof(line), "%s %s %u %s %s", tag, name,
                 rrs[i].ttl, tn, rdata);
        lines[count++] = strdup(line);
    }
    if (sorted) {
        qsort(lines, count, sizeof(char *), cmp_str);
    }
    for (size_t i = 0; i < count; i++) {
        printf("%s\n", lines[i]);
        free(lines[i]);
    }
    free(lines);
}

static void dump_msg(const firc_dns_msg_t *m)
{
    int opcode = (m->flags >> 11) & 0x0f;
    int rcode = m->flags & 0x0f;
    bool qr = (m->flags & FIRC_DNS_FLAG_QR) != 0;
    bool ra = (m->flags & FIRC_DNS_FLAG_RA) != 0;
    printf("id=%u qr=%s opcode=%d rcode=%d ra=%s\n", m->id,
           qr ? "true" : "false", opcode, rcode, ra ? "true" : "false");
    for (size_t i = 0; i < m->n_questions; i++) {
        char name[1024], tbuf[16];
        name_lower(m->questions[i].name, m->questions[i].name_len, name,
                   sizeof(name));
        const char *tn = type_name(m->questions[i].qtype, tbuf, sizeof(tbuf));
        printf("Q %s %s %u\n", name, tn, m->questions[i].qclass);
    }
    dump_section("AN", m->answers, m->n_answers, false);
    dump_section("NS", m->authority, m->n_authority, true);
    dump_section("AR", m->additional, m->n_additional, true);
}

int main(int argc, char **argv)
{
    if (argc != 2) {
        fprintf(stderr, "usage: firc-dnstool dump|stripaaaa|ptrcheck\n");
        return 2;
    }
    const char *cmd = argv[1];

    char *line = NULL;
    size_t cap = 0;
    ssize_t nread;
    static uint8_t raw[FIRC_DNS_MAX_MSG];

    while ((nread = getline(&line, &cap, stdin)) != -1) {
        while (nread > 0 && (line[nread - 1] == '\n' ||
                             line[nread - 1] == '\r' || line[nread - 1] == ' ')) {
            line[--nread] = '\0';
        }
        if (nread == 0 || line[0] == '#') {
            continue;
        }
        size_t rlen = 0;
        if (hex_decode(line, raw, sizeof(raw), &rlen) != 0) {
            printf("HEX_ERROR\n===\n");
            continue;
        }

        if (strcmp(cmd, "ptrcheck") == 0) {
            /* ptrcheck needs a full successful parse first */
            firc_dns_msg_t *m = NULL;
            if (firc_dns_msg_parse(raw, rlen, &m) != FIRC_OK) {
                printf("PARSE_ERROR\n");
            } else {
                firc_ip_t addr;
                printf("%s\n", firc_dns_ptr_query_addr(m, &addr) ? "ptr" : "no");
                firc_dns_msg_free(m);
            }
        } else if (strcmp(cmd, "dump") == 0) {
            firc_dns_msg_t *m = NULL;
            if (firc_dns_msg_parse(raw, rlen, &m) != FIRC_OK) {
                printf("PARSE_ERROR\n");
            } else {
                dump_msg(m);
                firc_dns_msg_free(m);
            }
        } else if (strcmp(cmd, "stripaaaa") == 0) {
            firc_dns_msg_t *m = NULL;
            if (firc_dns_msg_parse(raw, rlen, &m) != FIRC_OK) {
                printf("PARSE_ERROR\n");
            } else {
                firc_dns_msg_strip_aaaa(m);
                uint8_t *packed = NULL;
                size_t plen = 0;
                if (firc_dns_msg_pack(m, &packed, &plen) != FIRC_OK) {
                    printf("PACK_ERROR\n");
                } else {
                    firc_dns_msg_t *m2 = NULL;
                    if (firc_dns_msg_parse(packed, plen, &m2) != FIRC_OK) {
                        printf("REPARSE_ERROR\n");
                    } else {
                        dump_msg(m2);
                        firc_dns_msg_free(m2);
                    }
                    free(packed);
                }
                firc_dns_msg_free(m);
            }
        }
        printf("===\n");
    }
    free(line);
    return 0;
}
