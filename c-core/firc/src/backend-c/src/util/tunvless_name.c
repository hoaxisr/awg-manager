#include "firc/tunvless_name.h"

#include <stdbool.h>
#include <string.h>

static const struct {
    const char *stem;
    int max;
} stems[] = {{"tunvless", 99}, {"opkgtun", FIRC_OPKGTUN_MAX}};

bool firc_tundev_is_opkg(const char *name) { return name != NULL && strncmp(name, "opkgtun", 7) == 0; }

int firc_tundev_name_index(const char *name) {
    if (name == NULL) { return -1; }
    for (size_t i = 0; i < sizeof stems / sizeof stems[0]; i++) {
        size_t n = strlen(stems[i].stem);
        if (strncmp(name, stems[i].stem, n) != 0) { continue; }
        const char *d = name + n;
        if (d[0] < '0' || d[0] > '9') { return -1; }
        int v = d[0] - '0';
        if (d[1] != '\0') {
            if (d[0] == '0' || d[1] < '0' || d[1] > '9' || d[2] != '\0') { return -1; }
            v = v * 10 + (d[1] - '0');
        }
        return v <= stems[i].max ? v : -1;
    }
    return -1;
}
