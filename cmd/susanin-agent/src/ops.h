#ifndef SUSANIN_OPS_H
#define SUSANIN_OPS_H

#include "config.h"

int ops_setup(const susanin_config *cfg, const char *conf_path, int argc, char **argv);
int ops_status(const susanin_config *cfg, const char *conf_path);
int ops_apply(const susanin_config *cfg, const char *conf_path, int dry_run);
int ops_forget(const susanin_config *cfg, const char *ip);
const char *ops_default_conf_path(void);

#endif
