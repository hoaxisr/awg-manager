#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/duration.h"
#include "firc/log.h"
#include "firc/match.h"
#include "firc/models.h"
#include "firc/subparse.h"
#include "firc/tunnels.h"
#include "firc/yamlio.h"

static int cmd_resave(const char *path, const char *version)
{
    firc_config_t cfg;
    if (firc_config_init_defaults(&cfg) != FIRC_OK) {
        printf("ERROR\n");
        return 0;
    }
    firc_err_t err = firc_config_load_file(&cfg, path);
    if (err == FIRC_ERR_NOENT) {
        /* missing file keeps defaults */
        err = FIRC_OK;
    }
    if (err != FIRC_OK) {
        printf("ERROR\n");
        firc_config_clear(&cfg);
        return 0;
    }
    char *out = NULL;
    size_t out_len = 0;
    err = firc_config_save_buffer(&cfg, version, &out, &out_len);
    if (err != FIRC_OK) {
        printf("ERROR\n");
        firc_config_clear(&cfg);
        return 0;
    }
    fwrite(out, 1, out_len, stdout);
    free(out);
    firc_config_clear(&cfg);
    return 0;
}

static int cmd_match(void)
{
    char line[4096];
    while (fgets(line, sizeof(line), stdin) != NULL) {
        size_t len = strlen(line);
        if (len > 0 && line[len - 1] == '\n') {
            line[--len] = '\0';
        }
        if (len == 0 || line[0] == '#') {
            continue;
        }
        char *type = line;
        char *rule = strchr(type, '\t');
        if (rule == NULL) {
            continue;
        }
        *rule++ = '\0';
        char *domain = strchr(rule, '\t');
        if (domain == NULL) {
            continue;
        }
        *domain++ = '\0';

        firc_rule_matcher_t *m = firc_rule_matcher_new(type, rule);
        if (m == NULL) {
            printf("%s\t%s\t%s\tERROR\n", type, rule, domain);
            continue;
        }
        bool ok = firc_rule_matcher_match(m, domain);
        printf("%s\t%s\t%s\t%s\n", type, rule, domain,
               ok ? "match" : "nomatch");
        firc_rule_matcher_free(m);
    }
    return 0;
}

static int cmd_subparse(void)
{
    char *input = NULL;
    size_t cap = 0, len = 0;
    char chunk[4096];
    size_t n;
    while ((n = fread(chunk, 1, sizeof(chunk), stdin)) > 0) {
        if (len + n + 1 > cap) {
            cap = (cap == 0 ? 8192 : cap * 2);
            while (len + n + 1 > cap) {
                cap *= 2;
            }
            char *grown = realloc(input, cap);
            if (grown == NULL) {
                free(input);
                return 1;
            }
            input = grown;
        }
        memcpy(input + len, chunk, n);
        len += n;
    }
    if (input == NULL) {
        input = calloc(1, 1);
        if (input == NULL) {
            return 1;
        }
    } else {
        input[len] = '\0';
    }

    firc_sub_rules_t rules;
    firc_sub_rules_init(&rules);
    size_t dropped = 0;
    if (firc_sub_parse_rules_counted(input, &rules, &dropped) != FIRC_OK) {
        printf("ERROR\n");
        free(input);
        return 0;
    }
    for (size_t i = 0; i < rules.n; i++) {
        printf("%s|%s|%s\n", firc_sub_rules_type(&rules, i), firc_sub_rules_text(&rules, i),
               firc_sub_rules_enable(&rules, i) ? "true" : "false");
    }
    firc_sub_rules_free(&rules);
    /* Emits the drop count so the differential corpus covers refused lines. */
    printf("dropped|%zu\n", dropped);
    free(input);
    return 0;
}

static int cmd_duration(void)
{
    char line[256];
    while (fgets(line, sizeof(line), stdin) != NULL) {
        size_t len = strlen(line);
        if (len > 0 && line[len - 1] == '\n') {
            line[--len] = '\0';
        }
        if (len == 0) {
            continue;
        }
        firc_duration_t d;
        if (firc_duration_parse(line, &d) != FIRC_OK) {
            printf("%s\tERROR\n", line);
            continue;
        }
        char buf[40];
        firc_duration_format(d, buf, sizeof(buf));
        printf("%s\t%s\n", line, buf);
    }
    return 0;
}

static int cmd_tunnels_resave(const char *in, const char *out)
{
    firc_tunnels_t t;
    firc_tun_err_t e = {{0}, {0}};
    firc_err_t err = firc_tunnels_load_file(&t, in, &e);
    if (err == FIRC_OK) {
        err = firc_tunnels_save_file(&t, out);
        firc_tunnels_free(&t);
        e.where[0] = 0;
        e.why[0] = 0;
    }
    if (err == FIRC_OK) {
        return 0;
    }
    if (e.where[0] != 0 || e.why[0] != 0) {
        printf("ERROR %s: %s\n", e.where, e.why);
    } else {
        printf("ERROR %s\n", firc_err_str(err));
    }
    return 1;
}

int main(int argc, char **argv)
{
    firc_log_set_fd(2); /* keep stdout clean for differential comparison */
    if (argc >= 2 && strcmp(argv[1], "resave") == 0 && argc == 4) {
        return cmd_resave(argv[2], argv[3]);
    }
    if (argc == 4 && strcmp(argv[1], "tunnels-resave") == 0) {
        return cmd_tunnels_resave(argv[2], argv[3]);
    }
    if (argc == 2 && strcmp(argv[1], "match") == 0) {
        return cmd_match();
    }
    if (argc == 2 && strcmp(argv[1], "subparse") == 0) {
        return cmd_subparse();
    }
    if (argc == 2 && strcmp(argv[1], "duration") == 0) {
        return cmd_duration();
    }
    fprintf(stderr,
            "usage: firc-configtool resave <file> <version> | tunnels-resave <in> <out> | match | "
            "subparse | duration\n");
    return 2;
}
