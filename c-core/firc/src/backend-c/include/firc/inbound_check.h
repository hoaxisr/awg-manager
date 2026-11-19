#ifndef FIRC_INBOUND_CHECK_H
#define FIRC_INBOUND_CHECK_H

#include <stdbool.h>

#include "firc/err.h"
#include "firc/models.h"
#include "firc/tunnels.h"
#include "firc/tunrun.h"

/* WAN role of each of the n link entries, asked of RCI; false when RCI cannot answer (every entry then reads as not
   WAN). A firc_tunrun_wan_fn: the supervisor calls it once, when an inbound is first judged. */
bool firc_inbounds_wan_table(const char *const *link, size_t n, bool *wan, void *ud);

/* Save-time refusals for the inbounds of want: listen a LAN entry of the running app.link as the supervisor judges it,
   port not the WebUI's, the DNS proxy's or 53, and a bind probe unless an enabled running tunnel holds the same listen
   and port; FIRC_ERR_INVAL with err set. The first judged listen may read the WAN roles over RCI (firc_tunrun_lan_ok). */
firc_err_t firc_inbounds_check(const firc_tunnels_t *want, firc_tunrun_t *run, const firc_app_config_t *app,
                               firc_tun_err_t *err);

#endif
