#include "firc/ifacename.h"

#include <net/if.h>
#include <string.h>

bool firc_is_interface_name(const char *s) {
    if (s == NULL) { return false; }
    size_t n = strlen(s);
    if (n == 0 || n >= IF_NAMESIZE) { return false; }
    /* Refused: an interface name is also a sysfs path component. */
    if (strcmp(s, ".") == 0 || strcmp(s, "..") == 0) { return false; }
    for (size_t i = 0; i < n; i++) {
        unsigned char c = (unsigned char)s[i];
        bool ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
                  c == '.' || c == '_' || c == '-' || c == '@';
        if (!ok) { return false; }
    }
    return true;
}

void firc_interface_name_for_log(const char *s, char *out, size_t cap) {
    if (out == NULL || cap == 0) { return; }
    size_t i = 0;
    if (s != NULL) {
        for (; s[i] != '\0' && i + 1 < cap; i++) {
            unsigned char c = (unsigned char)s[i];
            out[i] = (char)((c >= 0x20 && c < 0x7f) ? c : '?');
        }
    }
    out[i] = '\0';
}
