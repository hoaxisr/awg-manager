#include "greatest.h"

#include <stdlib.h>
#include <string.h>

#include "firc/keenetic_rci.h"

static const firc_kn_iface_meta_t *find_meta(const firc_kn_iface_meta_t *arr, size_t n, const char *id) {
    for (size_t i = 0; i < n; i++) {
        if (strcmp(arr[i].id, id) == 0) { return &arr[i]; }
    }
    return NULL;
}

TEST parse_interface_list_detects_wan_types(void) {
    static const char *const json =
        "{"
        "\"GigabitEthernet1\":{\"type\":\"GigabitEthernet\",\"description\":\"Broadband\",\"interface-name\":\"ISP\",\"security-level\":\"public\",\"global\":true,\"defaultgw\":true,\"traits\":[\"Mac\",\"Ethernet\",\"Ip\",\"GigabitEthernet\"]},"
        "\"GigabitEthernet2\":{\"type\":\"GigabitEthernet\",\"description\":\"Backup ISP\",\"interface-name\":\"ISP2\",\"security-level\":\"public\",\"global\":false,\"traits\":[\"Mac\",\"Ethernet\",\"Ip\"]},"
        "\"PPPoE0\":{\"type\":\"PPPoE\",\"description\":\"Rostelecom\",\"interface-name\":\"PPPoE0\",\"security-level\":\"public\",\"global\":true,\"traits\":[\"Ip\",\"Ppp\",\"Pppoe\"]},"
        "\"PPPoE1\":{\"type\":\"PPPoE\",\"description\":\"Backup PPPoE\",\"interface-name\":\"PPPoE1\",\"security-level\":\"public\",\"global\":false,\"role\":[\"inet\"],\"traits\":[\"Ip\",\"Ppp\",\"Pppoe\"]},"
        "\"UsbQmi0\":{\"type\":\"UsbQmi\",\"description\":\"LTE Modem\",\"interface-name\":\"UsbQmi0\",\"security-level\":\"public\",\"global\":true,\"traits\":[\"Ip\",\"UsbQmi\"]},"
        "\"UsbDsl0\":{\"type\":\"UsbDsl\",\"description\":\"ADSL\",\"interface-name\":\"UsbDsl0\",\"security-level\":\"public\",\"global\":true,\"traits\":[\"Ip\",\"Dsl\"]},"
        "\"IPoE0\":{\"type\":\"IPoE\",\"description\":\"IPoE\",\"interface-name\":\"IPoE0\",\"security-level\":\"public\",\"global\":true,\"traits\":[\"Ip\"]},"
        "\"WifiMaster1/WifiStation0\":{\"type\":\"WifiStation\",\"description\":\"WISP\",\"interface-name\":\"WISP\",\"security-level\":\"public\",\"global\":true,\"traits\":[\"Wireless\",\"WifiStation\"]},"
        "\"L2TP0\":{\"type\":\"L2TP\",\"description\":\"ISP L2TP\",\"interface-name\":\"L2TP0\",\"security-level\":\"public\",\"defaultgw\":true,\"role\":[{\"role\":\"inet\",\"for\":\"GigabitEthernet2\"}],\"traits\":[\"Ip\",\"Ppp\",\"L2tp\"]}"
        "}";

    firc_kn_iface_meta_t *metas = NULL;
    size_t n = 0;
    ASSERT_EQ(FIRC_OK, firc_kn_parse_interface_list(json, &metas, &n));
    ASSERT_EQ((size_t)9, n);

    const firc_kn_iface_meta_t *m;
    m = find_meta(metas, n, "GigabitEthernet1");
    ASSERT(m != NULL && m->inet);
    m = find_meta(metas, n, "GigabitEthernet2");
    ASSERT(m != NULL && m->inet);
    m = find_meta(metas, n, "PPPoE0");
    ASSERT(m != NULL && m->inet);
    m = find_meta(metas, n, "PPPoE1");
    ASSERT(m != NULL && m->inet);
    m = find_meta(metas, n, "UsbQmi0");
    ASSERT(m != NULL && m->inet);
    m = find_meta(metas, n, "UsbDsl0");
    ASSERT(m != NULL && m->inet);
    m = find_meta(metas, n, "IPoE0");
    ASSERT(m != NULL && m->inet);
    m = find_meta(metas, n, "WifiMaster1/WifiStation0");
    ASSERT(m != NULL && m->inet);
    m = find_meta(metas, n, "L2TP0");
    ASSERT(m != NULL && m->inet);

    free(metas);
    PASS();
}

TEST parse_interface_list_excludes_vpn_and_local_entries(void) {
    static const char *const json =
        "{"
        "\"WifiMaster0/WifiStation0\":{\"type\":\"WifiStation\",\"description\":\"\",\"interface-name\":\"WifiMaster0/WifiStation0\",\"security-level\":\"public\",\"global\":false,\"traits\":[\"Wireless\",\"Ethernet\",\"WifiStation\"]},"
        "\"Bridge0\":{\"type\":\"Bridge\",\"description\":\"Home\",\"interface-name\":\"Home\",\"security-level\":\"private\",\"global\":true,\"role\":[\"inet\"],\"traits\":[\"Ethernet\",\"Bridge\"]},"
        "\"Bridge1\":{\"type\":\"Bridge\",\"description\":\"DMZ\",\"interface-name\":\"DMZ\",\"security-level\":\"public\",\"global\":true,\"role\":[\"inet\"],\"traits\":[\"Ethernet\",\"Bridge\"]},"
        "\"GigabitEthernet0/Vlan1\":{\"type\":\"Vlan\",\"description\":\"LAN VLAN\",\"interface-name\":\"Vlan1\",\"security-level\":\"private\",\"global\":true,\"role\":[\"inet\"],\"traits\":[\"Ethernet\"]},"
        "\"GigabitEthernet0/Vlan2\":{\"type\":\"Vlan\",\"description\":\"Guest VLAN\",\"interface-name\":\"Vlan2\",\"security-level\":\"protected\",\"global\":true,\"role\":[\"inet\"],\"traits\":[\"Ethernet\"]},"
        "\"0\":{\"type\":\"Port\",\"description\":\"Port 0\",\"interface-name\":\"0\",\"security-level\":\"public\",\"global\":true,\"role\":[\"inet\"],\"traits\":[\"Ethernet\"]},"
        "\"WifiMaster0\":{\"type\":\"WifiMaster\",\"description\":\"Radio\",\"interface-name\":\"WifiMaster0\",\"security-level\":\"public\",\"global\":true,\"traits\":[\"Ethernet\",\"WifiMaster\"]},"
        "\"WifiMaster0/AccessPoint0\":{\"type\":\"AccessPoint\",\"description\":\"Home Wi-Fi\",\"interface-name\":\"AccessPoint\",\"security-level\":\"public\",\"global\":true,\"role\":[\"inet\"],\"traits\":[\"Ethernet\",\"AccessPoint\"]},"
        "\"OpkgTun11\":{\"type\":\"OpkgTun\",\"description\":\"WARP\",\"interface-name\":\"OpkgTun11\",\"security-level\":\"public\",\"global\":true,\"defaultgw\":true,\"role\":[\"inet\"],\"traits\":[\"Ip\",\"OpkgTun\"]},"
        "\"Wireguard1\":{\"type\":\"Wireguard\",\"description\":\"WG\",\"interface-name\":\"Wireguard1\",\"security-level\":\"public\",\"global\":true,\"role\":[\"inet\"],\"traits\":[\"Ip\",\"Wireguard\"]},"
        "\"Proxy0\":{\"type\":\"Proxy\",\"description\":\"SB\",\"interface-name\":\"Proxy0\",\"security-level\":\"public\",\"global\":true,\"role\":[{\"role\":\"inet\",\"for\":\"OpkgTun11\"},{\"role\":\"inet\",\"for\":\"Bridge1\"},{\"role\":\"inet\",\"for\":\"GigabitEthernet0/Vlan2\"}],\"traits\":[\"Ip\",\"Proxy\"]},"
        "\"SSTP0\":{\"type\":\"SSTP\",\"description\":\"Dacha\",\"interface-name\":\"SSTP0\",\"security-level\":\"public\",\"global\":true,\"defaultgw\":true,\"role\":[\"inet\"],\"traits\":[\"Ip\",\"Ppp\",\"EncryptedPppTunnel\",\"Sstp\"]},"
        "\"L2TP1\":{\"type\":\"L2TP\",\"description\":\"L2TP/IPsec\",\"interface-name\":\"L2TP1\",\"security-level\":\"public\",\"global\":true,\"defaultgw\":true,\"role\":[\"inet\"],\"traits\":[\"Ip\",\"Ppp\",\"EncryptedPppTunnel\",\"L2tp\"]},"
        "\"OpenVPN0\":{\"type\":\"OpenVPN\",\"description\":\"OVPN\",\"interface-name\":\"OpenVPN0\",\"security-level\":\"public\",\"global\":true,\"defaultgw\":true,\"role\":[\"inet\"],\"traits\":[\"Ip\",\"OpenVPN\"]},"
        "\"IKE0\":{\"type\":\"IKE\",\"description\":\"IKEv2\",\"interface-name\":\"IKE0\",\"security-level\":\"public\",\"global\":true,\"defaultgw\":true,\"role\":[\"inet\"],\"traits\":[\"Ip\",\"Ipsec\"]},"
        "\"ZeroTier0\":{\"type\":\"ZeroTier\",\"description\":\"ZT\",\"interface-name\":\"ZeroTier0\",\"security-level\":\"public\",\"global\":true,\"defaultgw\":true,\"role\":[\"inet\"],\"traits\":[\"Ip\",\"ZeroTier\"]}"
        "}";

    firc_kn_iface_meta_t *metas = NULL;
    size_t n = 0;
    ASSERT_EQ(FIRC_OK, firc_kn_parse_interface_list(json, &metas, &n));
    ASSERT_EQ((size_t)16, n);

    for (size_t i = 0; i < n; i++) {
        ASSERT(!metas[i].inet);
    }

    free(metas);
    PASS();
}

SUITE(keenetic_rci_suite) {
    RUN_TEST(parse_interface_list_detects_wan_types);
    RUN_TEST(parse_interface_list_excludes_vpn_and_local_entries);
}

GREATEST_MAIN_DEFS();

int main(int argc, char **argv) {
    GREATEST_MAIN_BEGIN();
    RUN_SUITE(keenetic_rci_suite);
    GREATEST_MAIN_END();
}
