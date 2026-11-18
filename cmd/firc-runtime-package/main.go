// firc-runtime-package builds an independently removable, target-verified IPK.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"os"
)

func main() {
	source := flag.String("source", ".", "awg-manager source root (FIRC_SOURCE_ROOT selects pinned upstream assets)")
	daemon := flag.String("fircd", "", "target fircd ELF")
	tunnel := flag.String("tunvless", "", "target tunvless ELF")
	arch := flag.String("arch", "", "Entware architecture")
	version := flag.String("version", "", "runtime package version")
	out := flag.String("out", "", "output IPK")
	releaseURL := flag.String("url", "", "final HTTPS release URL (metadata only; never fetched)")
	flag.Parse()
	if *daemon == "" || *tunnel == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "-fircd, -tunvless and -out are required")
		os.Exit(2)
	}
	if err := firc.BuildRuntimeIPK(*source, *daemon, *tunnel, *arch, *version, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sum, err := firc.ComputeFileSHA256(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	stat, err := os.Stat(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	spec := firc.FircBinarySpec{Version: *version, PackageURL: *releaseURL, PackageSHA256: sum, PackageSize: stat.Size()}
	if *releaseURL != "" && !spec.IsAvailable() {
		fmt.Fprintln(os.Stderr, "invalid final HTTPS release metadata")
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]firc.FircBinarySpec{*arch: spec}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
