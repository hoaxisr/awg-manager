#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/opkgtun.h"

#include <errno.h>
#include <fcntl.h>
#include <linux/if_tun.h>
#include <net/if.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <time.h>
#include <unistd.h>

#include "firc/keenetic_rci.h"
#include "firc/tunaddr.h"

#define NAP_MS 50

static firc_err_t rci_get(void *ud, const char *path, char **body)
{
    firc_opkgtun_rci_t *r = ud;
    return firc_kn_rci_get_timeout(r->base_url, path, FIRC_KN_RCI_TIMEOUT_SECONDS, &r->stop, body);
}

static firc_err_t rci_post(void *ud, const char *body, long timeout_s, char **out)
{
    firc_opkgtun_rci_t *r = ud;
    return firc_kn_rci_post_cut(r->base_url, "/rci/", body, timeout_s, &r->stop, &r->cut, out);
}

static bool dev_exists(void *ud, const char *dev)
{
    (void)ud;
    return if_nametoindex(dev) != 0;
}

static firc_err_t unpersist(void *ud, const char *dev)
{
    (void)ud;
    int fd = open("/dev/net/tun", O_RDWR | O_CLOEXEC);
    if (fd < 0) {
        return firc_err_from_errno(errno);
    }
    struct ifreq ifr;
    memset(&ifr, 0, sizeof ifr);
    ifr.ifr_flags = IFF_TUN | IFF_NO_PI;
    snprintf(ifr.ifr_name, sizeof ifr.ifr_name, "%s", dev);
    firc_err_t err = FIRC_OK;
    if (ioctl(fd, TUNSETIFF, &ifr) != 0 || ioctl(fd, TUNSETPERSIST, 0) != 0) {
        err = firc_err_from_errno(errno);
    }
    close(fd);
    return err;
}

static bool addr_holder(void *ud, int n, const char *own, char out[16])
{
    (void)ud;
    return firc_tun_addr_holder(n, own, out) > 0;
}

static int64_t mono_ms(void *ud)
{
    (void)ud;
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static bool nap(void *ud, int ms)
{
    firc_opkgtun_rci_t *r = ud;
    for (int left = ms; left > 0; left -= NAP_MS) {
        if (atomic_load(&r->stop)) {
            return false;
        }
        int d = left < NAP_MS ? left : NAP_MS;
        struct timespec ts = {0, (long)d * 1000000L};
        while (nanosleep(&ts, &ts) != 0 && errno == EINTR) {
        }
    }
    return !atomic_load(&r->stop);
}

void firc_opkgtun_rci_io(firc_opkgtun_rci_t *rci, const char *base_url, const char *intent_path,
                         firc_opkgtun_io_t *io)
{
    atomic_init(&rci->stop, false);
    atomic_init(&rci->cut, false);
    snprintf(rci->base_url, sizeof rci->base_url, "%s", base_url);
    memset(io, 0, sizeof *io);
    io->get = rci_get;
    io->post = rci_post;
    io->dev_exists = dev_exists;
    io->unpersist = unpersist;
    io->addr_holder = addr_holder;
    io->now_ms = mono_ms;
    io->sleep_ms = nap;
    io->intent_path = intent_path;
    io->stop = &rci->stop;
    io->cut = &rci->cut;
    io->ud = rci;
}
