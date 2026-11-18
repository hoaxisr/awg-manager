#!/usr/bin/env python3
"""Allow private diagnostic SOCKS on lo without adding lo to app.link.

Run against the build copy only. Other interfaces still require the running
LAN whitelist; accepting an IP literal or wildcard would expose the listener.
"""
import pathlib
import sys

path = pathlib.Path(sys.argv[1]) / "src/backend-c/src/tunnels/tunrun.c"
source = path.read_text(encoding="utf-8")
start = source.index("static bool lan_ok(")
end = source.index("\nbool firc_tunrun_lan_ok", start)
body = source[start:end]
anchor = "    return false;\n}"
guard = '    /* AWGM: private diagnostics do not participate in LAN interception. */\n    return strcmp(name, "lo") == 0;\n}'
if guard not in body:
    if body.count(anchor) != 1:
        raise SystemExit("FIRC loopback patch: unexpected lan_ok implementation")
    path.write_text(source[:start] + body.replace(anchor, guard, 1) + source[end:], encoding="utf-8")
