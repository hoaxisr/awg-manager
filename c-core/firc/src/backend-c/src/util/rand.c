#include "firc/rand.h"

#include <fcntl.h>
#include <unistd.h>

firc_err_t firc_random_bytes(uint8_t *buf, size_t len) {
    /* Reads /dev/urandom directly: getrandom(2) needs kernel 3.17+ and some targets run 3.2/3.4. */
    int fd = open("/dev/urandom", O_RDONLY | O_CLOEXEC);
    if (fd < 0) { return FIRC_ERR_SYS; }
    size_t got = 0;
    while (got < len) {
        ssize_t n = read(fd, buf + got, len - got);
        if (n <= 0) { break; }
        got += (size_t)n;
    }
    close(fd);
    return got == len ? FIRC_OK : FIRC_ERR_SYS;
}
