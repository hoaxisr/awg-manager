// scripttest is a manual, live-network smoke test for one script
// transport: it loads a signed .js file exactly as the real factory does,
// starts it against a real URL, and prints every state/event transition
// plus received packets until the deadline. It exists because verifying a
// new script port (auth flow, wire framing, reconnect) against production
// infrastructure is a repeatable need - not just for mailru, for every
// transport this package ever gets.
//
//	scripttest -script transport/script/js/mailru.js -pubkey <hex> \
//	  -url "Vuri/d5nuZ5aQp" -duration 45s -send "hello"
//
// -cookies-file lets a captcha solved out-of-band (phone/WebView) unblock a
// yandex-family transport's happy path, exactly like the real app relaying
// a solved captcha's cookies back via ApplyCookies.
//
// A script that asks for setup (raise("needsSetup" | "captchaRequired", ...))
// is answered the way an app would: its page is served on 127.0.0.1 with
// window.openfluxSubmit in it (-open shows it in your browser), and what the
// page submits goes back to the script as cookiesApplied. -submit does the
// submitting for you, for a script run without a person. See
// transport/script/devhost and docs/scripted-transports.md.
//
// -settings does not start the transport: it opens the script's settings
// wizard (the page an app shows under "Настройки": the script's own
// Transport.settings(), else the form generated from info().params), prefilled
// from -param, and prints what a Save would hand to the script. -lang ru|en.
//
// -burst/-burst-size measure real submission throughput: pick a count well
// above the write queue's 1024-slot buffer (transport.DefaultConfig's
// MaxQueueSize), or the measurement is just "how fast can I fill a buffer",
// not the transport's actual sustained rate.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/transport/script"
	"github.com/p1neappleXpress/OpenFlux/transport/script/devhost"
)

func main() {
	scriptPath := flag.String("script", "", "Path to the .js file (its <path>.sig must verify)")
	pubkeyHex := flag.String("pubkey", "", "Hex ed25519 public key the .sig must verify against")
	url := flag.String("url", "", "URL/weblink handed to the script's open() as cfg.url")
	paramsRaw := flag.String("param", "", "Extra cfg.params entries as key=value, comma-separated")
	duration := flag.Duration("duration", 30*time.Second, "How long to watch before stopping")
	sendText := flag.String("send", "", "If set, Send() this text once the transport reports connected")
	cookiesFile := flag.String("cookies-file", "",
		"JSON file shaped {\"<url>\": {\"cookieName\": \"value\", ...}} - applied via ApplyCookies "+
			"shortly after Start(), same as an app relaying a phone/WebView captcha solve back to the transport.")
	burstCount := flag.Int("burst", 0,
		"Once connected, send this many packets back-to-back as fast as Send() accepts them, then report "+
			"elapsed time and throughput. 0 (default) = no burst, just -send's single packet.")
	burstSize := flag.Int("burst-size", 512, "Payload size in bytes for each -burst packet")
	openPages := flag.Bool("open", false,
		"Open the setup pages the script raises in your browser (they are always served on 127.0.0.1 and the address printed)")
	autoSubmit := flag.String("submit", "",
		"JSON a setup page would submit, e.g. '{\"token\":\"abc\"}': delivered as soon as the script raises a setup page, as if the user had filled it in")
	settings := flag.Bool("settings", false, "Open the script's settings wizard instead of running it (prefilled from -param); prints what Save submits")
	lang := flag.String("lang", "ru", "ru | en: the words the generated settings page adds")
	flag.Parse()

	if *scriptPath == "" || *pubkeyHex == "" {
		fmt.Fprintln(os.Stderr, "usage: scripttest -script <path> -pubkey <hex> -url <url> [-duration 30s] [-send text]")
		os.Exit(2)
	}

	pub, err := script.DecodePublicKeyHex(*pubkeyHex)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pubkey:", err)
		os.Exit(1)
	}

	params := map[string]interface{}{}
	if *paramsRaw != "" {
		for _, kv := range strings.Split(*paramsRaw, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				params[k] = v
			}
		}
	}

	if *settings {
		os.Exit(runSettings(*scriptPath, *pubkeyHex, params, *lang, *duration, *openPages))
	}

	name := *scriptPath
	tr, err := script.New(name, *scriptPath, pub, *url, params, transport.DefaultConfig())
	if err != nil {
		fmt.Fprintln(os.Stderr, "New:", err)
		os.Exit(1)
	}

	tr.SetEventHandler(func(kind string, payload map[string]interface{}) {
		fmt.Printf("[%s] EVENT %s %+v\n", ts(), kind, payload)
	})
	hostOpts := devhost.Options{Logf: func(f string, a ...interface{}) {
		fmt.Printf("[%s] SETUP "+f+"\n", append([]interface{}{ts()}, a...)...)
	}}
	if *openPages {
		hostOpts.Open = openBrowser
	}
	if *autoSubmit != "" {
		hostOpts.AutoSubmit = []byte(*autoSubmit)
	}
	setupHost := devhost.New(tr, hostOpts)
	defer setupHost.Close()
	tr.SetErrorNotifier(setupHost.Notify)
	recv := 0
	tr.Receive(func(b []byte) {
		recv++
		fmt.Printf("[%s] RECV #%d %d bytes %q\n", ts(), recv, len(b), truncate(string(b), 200))
	})

	fmt.Printf("[%s] starting %s against %q ...\n", ts(), *scriptPath, *url)
	if err := tr.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Start:", err)
		os.Exit(1)
	}
	defer func() {
		fmt.Printf("[%s] stopping\n", ts())
		_ = tr.Stop()
	}()

	if *cookiesFile != "" {
		values, err := loadCookiesFile(*cookiesFile, *url)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cookies-file:", err)
			os.Exit(1)
		}
		if err := tr.ApplyCookies(values); err != nil {
			fmt.Fprintln(os.Stderr, "ApplyCookies:", err)
			os.Exit(1)
		}
		fmt.Printf("[%s] applied %d cookies from %s\n", ts(), len(values), *cookiesFile)
	}

	deadline := time.After(*duration)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	sent := false
	burstDone := false
	for {
		select {
		case <-tick.C:
			state, errMsg := tr.LastState()
			stats := tr.Stats()
			fmt.Printf("[%s] connected=%-5v state=%-12s err=%q sent=%dB recv=%dB reconnects=%d\n",
				ts(), tr.IsConnected(), state, errMsg, stats.BytesSent, stats.BytesReceived, stats.Reconnects)
			if tr.IsConnected() && !sent && *sendText != "" {
				if err := tr.Send([]byte(*sendText)); err != nil {
					fmt.Printf("[%s] Send error: %v\n", ts(), err)
				} else {
					fmt.Printf("[%s] Send OK: %q\n", ts(), *sendText)
				}
				sent = true
			}
			if tr.IsConnected() && !burstDone && *burstCount > 0 {
				burstDone = true
				runBurst(tr, *burstCount, *burstSize)
			}
		case <-deadline:
			fmt.Printf("[%s] duration elapsed, done. total recv=%d packets\n", ts(), recv)
			return
		}
	}
}

// loadCookiesFile reads {"<url>": {"name": "value", ...}} and returns the
// map for urlHint if present, else the map from the file's only entry -
// same shape scripttest -cookies-file expects.
func loadCookiesFile(path, urlHint string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var all map[string]map[string]string
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if v, ok := all[urlHint]; ok {
		return v, nil
	}
	for _, v := range all {
		return v, nil
	}
	return nil, fmt.Errorf("no entries in %s", path)
}

// runBurst sends n packets of size bytes back-to-back as fast as Send()
// accepts them (no pacing) and reports elapsed time/throughput - the
// "how fast can this transport actually move data" measurement.
func runBurst(tr *script.ScriptTransport, n, size int) {
	payload := make([]byte, size)
	_, _ = rand.Read(payload)

	fmt.Printf("[%s] burst: sending %d x %dB packets as fast as Send() accepts them...\n", ts(), n, size)
	start := time.Now()
	sentOK, failed := 0, 0
	for i := 0; i < n; i++ {
		if err := tr.Send(payload); err != nil {
			failed++
			time.Sleep(2 * time.Millisecond) // back off on a full queue, don't spin
			continue
		}
		sentOK++
	}
	elapsed := time.Since(start)
	bps := float64(sentOK*size) / elapsed.Seconds()
	pps := float64(sentOK) / elapsed.Seconds()
	fmt.Printf("[%s] burst done: sent=%d failed=%d elapsed=%s throughput=%.0f pkt/s (%.1f KB/s submit-side)\n",
		ts(), sentOK, failed, elapsed.Round(time.Millisecond), pps, bps/1024)
}

// openBrowser shows a URL in the developer's default browser.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func ts() string { return time.Now().Format("15:04:05.000") }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// runSettings serves the settings wizard of the script and prints each
// submission as the script would get it (normalized: defaults in, types
// checked) until the first valid Save or the deadline.
func runSettings(path, pubkey string, params map[string]interface{}, lang string, wait time.Duration, open bool) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "script:", err)
		return 1
	}
	var sig []byte
	if !(len(data) > 1 && data[0] == 'P' && data[1] == 'K') { // a bare .js has a detached .sig
		if sig, err = os.ReadFile(path + ".sig"); err != nil {
			fmt.Fprintln(os.Stderr, "signature:", err)
			return 1
		}
	}
	values := map[string]string{}
	for k, v := range params {
		values[k] = fmt.Sprint(v)
	}
	rep := script.BuildSettings(data, sig, pubkey, values, lang)
	if !rep.OK {
		fmt.Fprintf(os.Stderr, "settings page: %s (%s)\n", rep.Error, rep.Code)
		return 1
	}
	if info, err := script.Inspect(scriptSource(data)); err == nil {
		for _, p := range info.CheckParams() {
			fmt.Printf("[%s] WARN  %s\n", ts(), p)
		}
	}
	kind := "generated from info().params"
	if rep.Custom {
		kind = "the script's own Transport.settings()"
	}
	fmt.Printf("[%s] settings wizard of %s v%s (%s): %d settings\n", ts(), rep.Name, rep.Version, kind, len(rep.Params))

	saved := make(chan map[string]string, 1)
	opts := devhost.Options{Logf: func(f string, a ...interface{}) {
		fmt.Printf("[%s] SETUP "+f+"\n", append([]interface{}{ts()}, a...)...)
	}}
	if open {
		opts.Open = openBrowser
	}
	h := devhost.New(nil, opts)
	defer h.Close()
	h.ShowPage(rep.HTML, func(got map[string]string) error {
		clean, errs := script.NormalizeSettings(rep.Params, got)
		if len(errs) > 0 && !rep.Custom {
			return fmt.Errorf("%s", errs[0])
		}
		if rep.Custom {
			clean = got
		}
		select {
		case saved <- clean:
		default:
		}
		return nil
	})
	select {
	case got := <-saved:
		b, _ := json.MarshalIndent(got, "", "  ")
		fmt.Printf("[%s] SAVED, the script would get as cfg.params:\n%s\n", ts(), b)
		return 0
	case <-time.After(wait):
		fmt.Printf("[%s] nothing was saved within %s\n", ts(), wait)
		return 0
	}
}

// scriptSource is the JavaScript of a bare .js or of a .flux package.
func scriptSource(data []byte) []byte {
	if len(data) > 1 && data[0] == 'P' && data[1] == 'K' {
		if pkg, err := script.ReadPackage(data); err == nil {
			return pkg.Script
		}
	}
	return data
}
