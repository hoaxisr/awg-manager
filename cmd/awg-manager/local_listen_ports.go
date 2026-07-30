package main

import "github.com/hoaxisr/awg-manager/internal/proxylisten"

// crossListenPortChecker adapts proxylisten.CrossChecker for freeturn/wdtt services.
type crossListenPortChecker = proxylisten.CrossChecker
