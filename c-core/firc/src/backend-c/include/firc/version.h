#ifndef FIRC_VERSION_H
#define FIRC_VERSION_H

#ifndef FIRC_VERSION
#define FIRC_VERSION "unattached"
#endif

#ifndef FIRC_TARGET
#define FIRC_TARGET ""
#endif

#ifndef FIRC_REVISION
#define FIRC_REVISION 1
#endif

#define FIRC_STR_(x) #x
#define FIRC_STR(x) FIRC_STR_(x)
#define FIRC_REVISION_STR FIRC_STR(FIRC_REVISION)

#ifdef FIRC_ENTWARE_KN
#define FIRC_FLAVOUR "entware_kn"
#else
#define FIRC_FLAVOUR "host"
#endif

#define FIRC_VERSION_LINE \
    FIRC_VERSION " firc-flavour=" FIRC_FLAVOUR " target=" FIRC_TARGET " revision=" FIRC_REVISION_STR

#endif
