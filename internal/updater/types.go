package updater

import (
	"errors"
	"time"
)

// UpdateInfo holds the result of an update check.
type UpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	DownloadURL    string `json:"downloadUrl,omitempty"`
	// SHA256 is the expected ipk checksum from the repo Packages index.
	// The repo is served over plain HTTP, so verifying it before handing the
	// file to a root opkg install is the only integrity check we get.
	SHA256    string    `json:"sha256,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	Checking  bool      `json:"checking"`
	Error     string    `json:"error,omitempty"`
	Warning   string    `json:"warning,omitempty"`
}

var ErrUpgradeInProgress = errors.New("upgrade already in progress")

// ErrRestoreInProgress — идёт восстановление из резервной копии или снимка.
var ErrRestoreInProgress = errors.New("идёт восстановление из резервной копии — обновление отложено")
