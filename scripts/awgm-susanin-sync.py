#!/usr/bin/env python3
"""
awgm-susanin-sync.py - Dynamic real-time synchronizer between Susanin and Mihomo.
1. Monitors susanin_ok_tcp, susanin_ok_udp, susanin_ok_net ipsets and susanin.json whitelist.
2. Tails Mihomo log for direct dial failures (DPI stalls / timeouts).
   - If a domain name (FQDN 3rd-5th level) is detected, it is saved into the Susanin
     whitelist (susanin.json alwaysEntries and vpn_always.txt) and compiled as a DOMAIN rule.
     Underlying IP is also added to susanin_ok_tcp for instant retry unblock.
   - If a raw IP is detected, it is promoted to susanin_ok_tcp / susanin_ok_net ipsets.
3. Compiles all active rules into Mihomo classical rule-provider "susanin" and updates
   Mihomo in-memory via REST API with zero downtime.
"""

import os
import sys
import time
import signal
import subprocess
import urllib.request
import urllib.error
import re
import json
from collections import defaultdict

RULES_FILE = "/opt/etc/awg-manager/mihomo/rules/susanin.yaml"
RULES_TMP = "/opt/etc/awg-manager/mihomo/rules/susanin.yaml.tmp"
SINGBOX_RULES_FILE = "/opt/etc/awg-manager/sing-box/rules/susanin.json"
SINGBOX_RULES_TMP = "/opt/etc/awg-manager/sing-box/rules/susanin.json.tmp"
LOG_FILE = "/opt/var/log/susanin-sync.log"
PID_FILE = "/var/run/awgm-susanin-sync.pid"
MIHOMO_API = "http://127.0.0.1:9090/providers/rules/susanin"
MIHOMO_LOG = "/tmp/mihomo.log"
SUSANIN_JSON = "/opt/etc/awg-manager/susanin.json"
VPN_ALWAYS_PATHS = [
    "/opt/etc/awg-manager/susanin/vpn_always.txt",
    "/opt/susanin/etc/vpn_always.txt"
]
POLL_INTERVAL = 2  # seconds
MAX_LOG_SIZE = 512 * 1024  # 512 KB

LINE_REGEX = re.compile(
    r'dial DIRECT \(match [^)]*\)\s+[0-9.]+:[0-9]+\s+-->\s+([^:\s]+):[0-9]+\s+error:\s*(.*)'
)
DIAL_IP_REGEX = re.compile(
    r'dial (?:tcp|udp)\s+([0-9.]+):[0-9]+:\s*(?:i/o timeout|connection reset by peer|connection refused|no route to host)'
)

running = True

def sig_handler(signum, frame):
    global running
    running = False

signal.signal(signal.SIGTERM, sig_handler)
signal.signal(signal.SIGINT, sig_handler)

def log(msg):
    ts = time.strftime("%Y-%m-%d %H:%M:%S")
    line = f"[{ts}] {msg}\n"
    sys.stdout.write(line)
    sys.stdout.flush()
    try:
        if os.path.exists(LOG_FILE) and os.path.getsize(LOG_FILE) > MAX_LOG_SIZE:
            os.replace(LOG_FILE, LOG_FILE + ".old")
        with open(LOG_FILE, "a", encoding="utf-8", errors="replace") as f:
            f.write(line)
    except Exception:
        pass

def is_public_ip(ip_str):
    parts = ip_str.split('.')
    if len(parts) != 4:
        return False
    try:
        first = int(parts[0])
        second = int(parts[1])
        if first in (10, 127, 0):
            return False
        if first == 172 and 16 <= second <= 31:
            return False
        if first == 192 and second == 168:
            return False
        if first == 100 and 64 <= second <= 127:
            return False
        if first >= 224:
            return False
        return True
    except Exception:
        return False

def is_domain_name(target):
    if not target or len(target) < 3 or len(target) > 255:
        return False
    if re.match(r'^[0-9.]+$', target) or ':' in target:
        return False
    parts = target.split('.')
    if len(parts) < 2:
        return False
    last = parts[-1].lower()
    if last in ('lan', 'local', 'arpa', 'internal', 'home', 'box', 'invalid', 'localhost', 'keenetic'):
        return False
    for part in parts:
        if not part or len(part) > 63:
            return False
        if not re.match(r'^[a-z0-9]([a-z0-9-_]*[a-z0-9])?$', part, re.IGNORECASE):
            return False
    return True

def add_domain_to_whitelist(domain):
    domain = domain.strip().lower()
    if not is_domain_name(domain):
        return False

    # 1. Update susanin.json
    added = False
    try:
        data = {}
        if os.path.exists(SUSANIN_JSON):
            with open(SUSANIN_JSON, 'r', encoding='utf-8') as f:
                data = json.load(f)

        always = data.get("alwaysEntries", [])
        if not isinstance(always, list):
            always = []

        if not any(e.strip().lower() == domain for e in always):
            always.append(domain)
            data["alwaysEntries"] = always
            tmp_file = SUSANIN_JSON + ".tmp"
            with open(tmp_file, 'w', encoding='utf-8') as f:
                json.dump(data, f, indent=2, ensure_ascii=False)
            os.replace(tmp_file, SUSANIN_JSON)
            added = True
    except Exception as e:
        log(f"WARN: could not update {SUSANIN_JSON}: {e}")

    # 2. Update vpn_always.txt files
    for p in VPN_ALWAYS_PATHS:
        try:
            p_dir = os.path.dirname(p)
            if not os.path.exists(p_dir):
                continue
            lines = []
            if os.path.exists(p):
                with open(p, 'r', encoding='utf-8', errors='replace') as f:
                    lines = [line.strip() for line in f if line.strip()]
            if not any(l.lower() == domain for l in lines):
                lines.append(domain)
                tmp_p = p + ".tmp"
                with open(tmp_p, 'w', encoding='utf-8') as f:
                    for l in lines:
                        f.write(l + "\n")
                os.replace(tmp_p, p)
        except Exception as e:
            log(f"WARN: could not update {p}: {e}")

    return added

class MihomoLogRadar:
    def __init__(self, path):
        self.path = path
        self.file_pos = 0
        self.last_inode = None
        self.subnet_failures = defaultdict(list)
        try:
            if os.path.exists(self.path):
                st = os.stat(self.path)
                self.file_pos = st.st_size
                self.last_inode = st.st_ino
        except Exception:
            pass

    def scan(self):
        events = []
        if not os.path.exists(self.path):
            return events
        try:
            st = os.stat(self.path)
            if self.last_inode != st.st_ino or st.st_size < self.file_pos:
                self.file_pos = 0
                self.last_inode = st.st_ino

            if st.st_size == self.file_pos:
                return events

            with open(self.path, 'r', encoding='utf-8', errors='replace') as f:
                f.seek(self.file_pos)
                chunk = f.read(256 * 1024)
                self.file_pos = f.tell()
                for line in chunk.splitlines():
                    m = LINE_REGEX.search(line)
                    if not m:
                        continue
                    target = m.group(1).strip().lower()
                    err_body = m.group(2)
                    dial_ips = set(DIAL_IP_REGEX.findall(err_body))
                    if not dial_ips:
                        dial_ips = set(DIAL_IP_REGEX.findall(line))
                    valid_ips = {ip for ip in dial_ips if is_public_ip(ip)}
                    is_domain = is_domain_name(target)
                    events.append((target, valid_ips, is_domain))
        except Exception as e:
            log(f"Radar read error: {e}")
        return events

    def process_events(self, events):
        now = time.time()
        for target, dial_ips, is_domain in events:
            if is_domain:
                added = add_domain_to_whitelist(target)
                if added:
                    ip_info = f" (dial IPs: {', '.join(sorted(dial_ips))})" if dial_ips else ""
                    log(f"⚡ Radar: learned domain '{target}'{ip_info}, added to Susanin Whitelist & rules/susanin.yaml")
                # Immediately unblock client's in-flight retries
                for ip in dial_ips:
                    subprocess.run(["ipset", "add", "susanin_ok_tcp", ip, "-exist"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            else:
                # Raw IP target
                ips_to_add = dial_ips if dial_ips else ({target} if is_public_ip(target) else set())
                for ip in ips_to_add:
                    res = subprocess.run(["ipset", "add", "susanin_ok_tcp", ip, "-exist"],
                                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    if res.returncode == 0:
                        log(f"⚡ Radar: detected DPI stall on {ip}, promoted to susanin_ok_tcp")

                    # Check subnet clustering (/24)
                    parts = ip.split('.')
                    subnet = f"{parts[0]}.{parts[1]}.{parts[2]}"
                    history = self.subnet_failures[subnet]
                    history = [t for t in history if now - t < 300]
                    history.append(now)
                    self.subnet_failures[subnet] = history

                    if len(history) >= 2:
                        net_cidr = f"{subnet}.0/24"
                        r = subprocess.run(["ipset", "add", "susanin_ok_net", net_cidr, "-exist"],
                                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                        if r.returncode == 0:
                            log(f"⚡ Radar: clustered {len(history)} stalls in {net_cidr}, promoted subnet to susanin_ok_net")

def get_ipset_members(set_name):
    try:
        res = subprocess.run(["ipset", "list", set_name],
                             stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                             text=True, timeout=2)
        if res.returncode != 0:
            return set()
        members = set()
        in_members = False
        for line in res.stdout.splitlines():
            line = line.strip()
            if line.startswith("Members:"):
                in_members = True
                continue
            if in_members and line:
                val = line.split()[0]
                if val:
                    members.add(val)
        return members
    except Exception:
        return set()

def read_whitelist_entries():
    if not os.path.exists(SUSANIN_JSON):
        return []
    try:
        with open(SUSANIN_JSON, 'r', encoding='utf-8') as f:
            data = json.load(f)
            always = data.get("alwaysEntries", [])
            if isinstance(always, list):
                return [e.strip() for e in always if isinstance(e, str) and e.strip()]
    except Exception:
        pass
    return []

def format_classical_rule(item):
    item = item.strip()
    if not item:
        return None
    prefixes = ('DOMAIN,', 'DOMAIN-SUFFIX,', 'DOMAIN-KEYWORD,', 'IP-CIDR,', 'IP-CIDR6,', 'SRC-IP-CIDR,', 'GEOIP,', 'GEOSITE,')
    for p in prefixes:
        if item.upper().startswith(p):
            return item
    if is_domain_name(item):
        return f"DOMAIN,{item.lower()}"
    if '/' in item:
        return f"IP-CIDR,{item},no-resolve"
    if is_public_ip(item):
        return f"IP-CIDR,{item}/32,no-resolve"
    return None

def compile_all_rules():
    rules = set()
    # 1. Whitelist from susanin.json
    for entry in read_whitelist_entries():
        r = format_classical_rule(entry)
        if r:
            rules.add(r)

    # 2. Active ipset members
    tcp = get_ipset_members("susanin_ok_tcp")
    udp = get_ipset_members("susanin_ok_udp")
    net = get_ipset_members("susanin_ok_net")
    for ip in (tcp | udp | net):
        r = format_classical_rule(ip)
        if r:
            rules.add(r)

    return rules

def read_current_rules():
    if not os.path.exists(RULES_FILE):
        return set()
    try:
        entries = set()
        with open(RULES_FILE, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if line.startswith("- '") and line.endswith("'"):
                    entries.add(line[3:-1])
                elif line.startswith("- \"") and line.endswith("\""):
                    entries.add(line[3:-1])
        return entries
    except Exception:
        return set()

def notify_mihomo():
    try:
        req = urllib.request.Request(MIHOMO_API, method="PUT")
        with urllib.request.urlopen(req, timeout=3) as resp:
            return resp.status in (200, 204)
    except urllib.error.URLError:
        return False
    except Exception:
        return False

def notify_singbox():
    try:
        res = subprocess.run(["pidof", "sing-box"], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        if res.returncode == 0:
            pids = res.stdout.strip().split()
            for p in pids:
                if p.isdigit():
                    os.kill(int(p), signal.SIGHUP)
            return True
    except Exception as e:
        log(f"Singbox HUP error: {e}")
    return False

def sync_once(radar, last_rules):
    # 1. Scan Mihomo log and process failure events
    events = radar.scan()
    if events:
        radar.process_events(events)

    # 2. Compile all active classical rules
    current_rules = compile_all_rules()

    singbox_missing_or_empty = (
        not os.path.exists(SINGBOX_RULES_FILE) or
        os.path.getsize(SINGBOX_RULES_FILE) < 50
    )

    if not current_rules and not last_rules and not singbox_missing_or_empty:
        return last_rules

    if current_rules != last_rules or singbox_missing_or_empty:
        added = current_rules - last_rules if last_rules else current_rules
        removed = last_rules - current_rules if last_rules else set()

        # Write temporary classical YAML
        sorted_rules = sorted(current_rules)
        os.makedirs(os.path.dirname(RULES_FILE), exist_ok=True)
        with open(RULES_TMP, "w", encoding="utf-8") as f:
            f.write("payload:\n")
            for item in sorted_rules:
                f.write(f"  - '{item}'\n")
        os.replace(RULES_TMP, RULES_FILE)

        # Write Sing-box JSON rule-set
        # NOTE: In Sing-box rule-sets, fields within a single rule object are evaluated with AND!
        # Domain, domain_suffix, and ip_cidr MUST be in SEPARATE rule objects so they match via OR!
        domains = []
        domain_suffixes = []
        ip_cidrs = []
        for r in sorted_rules:
            parts = r.split(',')
            rtype = parts[0].strip().upper()
            val = parts[1].strip() if len(parts) > 1 else ""
            if rtype == "DOMAIN" and val:
                domains.append(val)
                domain_suffixes.append(val)
            elif rtype == "DOMAIN-SUFFIX" and val:
                domain_suffixes.append(val)
            elif (rtype == "IP-CIDR" or rtype == "IP-CIDR6") and val:
                ip_cidrs.append(val)

        singbox_rules = []
        if domains:
            singbox_rules.append({"domain": sorted(list(set(domains)))})
        if domain_suffixes:
            singbox_rules.append({"domain_suffix": sorted(list(set(domain_suffixes)))})
        if ip_cidrs:
            singbox_rules.append({"ip_cidr": sorted(list(set(ip_cidrs)))})

        singbox_payload = {"version": 1, "rules": singbox_rules}

        os.makedirs(os.path.dirname(SINGBOX_RULES_FILE), exist_ok=True)
        with open(SINGBOX_RULES_TMP, "w", encoding="utf-8") as f:
            json.dump(singbox_payload, f, indent=2)
        os.replace(SINGBOX_RULES_TMP, SINGBOX_RULES_FILE)

        # Signal Mihomo and Sing-box to reload in memory
        ok_mihomo = notify_mihomo()
        ok_sb = notify_singbox()
        mihomo_str = "Mihomo: OK" if ok_mihomo else "Mihomo: pending"
        sb_str = "Sing-box: HUP" if ok_sb else "Sing-box: offline"
        log(f"Sync: {len(sorted_rules)} total rules (+{len(added)} / -{len(removed)}): {mihomo_str}, {sb_str}")
        return current_rules

    return last_rules

def main():
    try:
        with open(PID_FILE, "w") as f:
            f.write(str(os.getpid()))
    except Exception as e:
        log(f"WARN: could not write PID file: {e}")

    log("awgm-susanin-sync daemon started (with active Mihomo domain & IP radar)")
    radar = MihomoLogRadar(MIHOMO_LOG)
    last_rules = read_current_rules()
    log(f"Initial rules loaded: {len(last_rules)} entries")

    # Initial sync on startup
    last_rules = sync_once(radar, last_rules)

    while running:
        try:
            last_rules = sync_once(radar, last_rules)
        except Exception as e:
            log(f"Error in sync loop: {e}")
        time.sleep(POLL_INTERVAL)

    log("awgm-susanin-sync daemon exiting")
    try:
        if os.path.exists(PID_FILE):
            os.remove(PID_FILE)
    except Exception:
        pass

if __name__ == "__main__":
    main()
