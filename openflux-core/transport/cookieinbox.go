package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// A cookie inbox is how the node's owner hands a running exit a new sign-in
// without restarting it: node-install.sh set-cookies (run as root over the
// owner's SSH access) writes the pushed store JSON ({document URL: {name:
// value}}) to CookieInboxPath(store) and sends the node SIGHUP; the node takes
// the file and merges each document's jar into its own (see
// manager.MergeSignIns). Only root and the node user can write there: the
// state directory is the node user's, and the file is 0600.

// CookieInboxPath is the inbox file next to the cookie store at storePath.
func CookieInboxPath(storePath string) string { return storePath + ".incoming" }

// cookieInboxMax bounds what is read: node-install.sh accepts 64 KiB of
// base64, which decodes to less.
const cookieInboxMax = 64 << 10

// TakeCookieInbox reads and removes the inbox at path. A missing inbox is
// (nil, nil). A file that is not a store JSON is removed too and reported, so
// a bad push is not retried on every signal. Errors never quote the content.
func TakeCookieInbox(path string) (map[string]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cookie inbox: %w", err)
	}
	data, rerr := io.ReadAll(io.LimitReader(f, cookieInboxMax+1))
	_ = f.Close()
	_ = os.Remove(path)
	if rerr != nil {
		return nil, fmt.Errorf("cookie inbox: %w", rerr)
	}
	if len(data) > cookieInboxMax {
		return nil, errors.New("cookie inbox: too large")
	}
	var byDoc map[string]map[string]string
	if json.Unmarshal(data, &byDoc) != nil {
		return nil, errors.New("cookie inbox: not a cookie store")
	}
	return byDoc, nil
}
