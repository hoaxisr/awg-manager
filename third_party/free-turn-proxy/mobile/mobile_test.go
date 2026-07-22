package mobile

import (
	"slices"
	"strings"
	"testing"
)

func TestClientArgsFreeturnURIIsPositional(t *testing.T) {
	uri := "freeturn://config"
	args := clientArgs(uri, "", "", "127.0.0.1:9000", "tcp", "")
	if !slices.Contains(args, uri) {
		t.Fatalf("args do not contain URI: %v", args)
	}
	if slices.Contains(args, "-link") {
		t.Fatalf("URI passed as -link: %v", args)
	}
	if args[len(args)-1] != uri {
		t.Fatalf("URI must follow all flags: %v", args)
	}
}

func TestClientArgsVKLink(t *testing.T) {
	link := "https://vk.ru/call/join/code"
	args := clientArgs(link, "1.2.3.4:56000", "", "127.0.0.1:9000", "tcp", "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-link "+link) || !strings.Contains(joined, "-peer 1.2.3.4:56000") {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestManualCaptchaRequiresPresenter(t *testing.T) {
	SetCaptchaPresenter(nil)
	SetManualCaptcha(true)
	t.Cleanup(func() { SetManualCaptcha(false) })

	err := Start("https://vk.ru/call/join/code", "1.2.3.4:56000", "", "", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "requires presenter") {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestStartFlagsManualCaptchaRequiresPresenter(t *testing.T) {
	SetCaptchaPresenter(nil)
	SetManualCaptcha(true)
	t.Cleanup(func() { SetManualCaptcha(false) })

	flags := "-listen\n127.0.0.1:9000\n-peer\n1.2.3.4:56000\n-link\nhttps://vk.ru/call/join/code"
	err := StartFlags(flags)
	if err == nil || !strings.Contains(err.Error(), "requires presenter") {
		t.Fatalf("StartFlags() error = %v", err)
	}
}
