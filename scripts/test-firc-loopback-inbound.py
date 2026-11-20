#!/usr/bin/env python3
"""Exercise the actual patched C whitelist function, without router privileges."""
import pathlib
import subprocess
import sys
import tempfile

source = (pathlib.Path(sys.argv[1]) / "src/backend-c/src/tunnels/tunrun.c").read_text()
start = source.index("static bool lan_ok(")
end = source.index("\nbool firc_tunrun_lan_ok", start)
with tempfile.TemporaryDirectory() as directory:
    root = pathlib.Path(directory)
    (root / "check.c").write_text('''#include <stdbool.h>
#include <stddef.h>
#include <string.h>
#include <assert.h>
typedef void (*firc_tunrun_wan_fn)(const char *const *, size_t, bool *, void *);
typedef struct { firc_tunrun_wan_fn wan_of; char **link; size_t n_link; bool *wan; void *wan_ud; } firc_tunrun_t;
''' + source[start:end] + '''
int main(void) {
    char *links[] = {"br0", "eth3"}; bool wan[] = {false, true};
    firc_tunrun_t t = {.link=links, .n_link=2, .wan=wan};
    assert(lan_ok(&t, "lo"));
    assert(lan_ok(&t, "br0"));
    assert(!lan_ok(&t, "eth3"));
    assert(!lan_ok(&t, "br1"));
    assert(!lan_ok(&t, "0.0.0.0"));
    assert(!lan_ok(&t, "127.0.0.1"));
    assert(!lan_ok(&t, "*"));
    links[1] = "lo";
    assert(!lan_ok(&t, "lo")); /* Never override an explicit WAN verdict. */
    return 0;
}
''')
    subprocess.run(["cc", "-Wall", "-Wextra", "-Werror", str(root / "check.c"), "-o", str(root / "check")], check=True)
    subprocess.run([str(root / "check")], check=True)
print("PASS: actual FIRC whitelist accepts lo and retains LAN/WAN restrictions")
