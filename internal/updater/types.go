package updater

import (
	"errors"
	"time"
)

// UpdateInfo holds the result of an update check.
type UpdateInfo struct {
	Available      bool      `json:"available"`
	CurrentVersion string    `json:"currentVersion"`
	LatestVersion  string    `json:"latestVersion,omitempty"`
	CheckedAt      time.Time `json:"checkedAt"`
	Checking       bool      `json:"checking"`
	Error          string    `json:"error,omitempty"`
}

var ErrUpgradeInProgress = errors.New("upgrade already in progress")
