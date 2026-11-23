package firc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const RuntimePackageName = "awg-firc-runtime"
const maxRuntimeIPKBytes int64 = 32 << 20

var runtimeVersionPattern = regexp.MustCompile(`^[0-9][A-Za-z0-9.+~_-]{0,95}$`)

func validRuntimeVersion(s string) bool { return runtimeVersionPattern.MatchString(s) }

type RuntimePackageManager interface {
	Install(context.Context, string) error
	Remove(context.Context, string) error
	Version(context.Context, string) (string, error)
}

// ExternalPackageRemover removes an allowlisted external Entware FIRC package
// (upstream "firc" or "tunvless") during explicit user-confirmed migration to awg-firc-runtime.
type ExternalPackageRemover interface {
	RemoveExternal(ctx context.Context, name string) error
}

func isAllowedExternalFircPackage(name string) bool {
	return name == "firc" || name == "tunvless"
}

// OpkgRuntimeManager never forces ownership conflicts and uses opkg's own lock.
// No command, package name or installation URL is accepted from the browser.
type OpkgRuntimeManager struct {
	StatusPath string
}

func (OpkgRuntimeManager) CheckEnvironment() error {
	info, err := os.Stat("/opt/bin/opkg")
	if err != nil {
		return fmt.Errorf("Entware opkg unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return errors.New("Entware opkg is not an executable file")
	}
	return nil
}

func readRuntimeIPKControlVersion(ipkPath string) (string, error) {
	raw, err := os.ReadFile(ipkPath)
	if err != nil {
		return "", err
	}
	outer, err := readRuntimeTar(raw, 2*maxRuntimeIPKBytes)
	if err != nil {
		return "", err
	}
	ctrlEntry, ok := outer["control.tar.gz"]
	if !ok {
		return "", errors.New("missing control.tar.gz in runtime IPK")
	}
	ctrl, err := readRuntimeTar(ctrlEntry.data, 1<<20)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(ctrl["control"].data), "\n") {
		key, val, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(key) == "Version" {
			return strings.TrimSpace(val), nil
		}
	}
	return "", errors.New("missing Version in runtime IPK control")
}

func (m OpkgRuntimeManager) Install(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("runtime package path must be absolute")
	}
	targetVer, err := readRuntimeIPKControlVersion(path)
	if err != nil {
		return fmt.Errorf("inspect runtime package before opkg install: %w", err)
	}
	installedVer, verErr := m.Version(ctx, RuntimePackageName)
	if verErr != nil || (installedVer != "" && installedVer == targetVer) {
		// Same-version repair or half-installed recovery: remove awg-firc-runtime
		// first (conffiles is empty so user /opt/etc/firc/* configs are untouched)
		// instead of prohibited reinstall flags, while keeping file-clash checks active.
		if rmOut, rmErr := exec.CommandContext(ctx, "/opt/bin/opkg", "remove", RuntimePackageName).CombinedOutput(); rmErr != nil && verErr == nil {
			return fmt.Errorf("opkg runtime remove before repair: %w: %.2048s", rmErr, rmOut)
		}
	}
	out, err := exec.CommandContext(ctx, "/opt/bin/opkg", "install", "--force-downgrade", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("opkg runtime install: %w: %.2048s", err, out)
	}
	return nil
}
func (OpkgRuntimeManager) Remove(ctx context.Context, name string) error {
	if name != RuntimePackageName {
		return errors.New("refusing to remove unrelated package")
	}
	out, err := exec.CommandContext(ctx, "/opt/bin/opkg", "remove", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("opkg runtime remove: %w: %.2048s", err, out)
	}
	return nil
}

// RemoveExternal uninstalls an allowlisted external FIRC package using plain `opkg remove`
// (never --force-*), after checking its opkg info list is present.
func (m OpkgRuntimeManager) RemoveExternal(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !isAllowedExternalFircPackage(name) {
		return fmt.Errorf("refusing to remove unsupported external FIRC package %q", name)
	}
	infoDir := "/opt/lib/opkg/info"
	if sp := strings.TrimSpace(m.StatusPath); sp != "" {
		infoDir = filepath.Join(filepath.Dir(sp), "info")
	}
	listPath := filepath.Join(infoDir, name+".list")
	if st, err := os.Stat(listPath); err != nil {
		return fmt.Errorf("external package list unavailable (%s): %w", listPath, err)
	} else if st.IsDir() {
		return fmt.Errorf("invalid external package list path: %s", listPath)
	}
	out, err := exec.CommandContext(ctx, "/opt/bin/opkg", "remove", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("opkg external package remove (%s): %w: %.2048s", name, err, out)
	}
	return nil
}
func parseOpkgStatusTuple(statusRaw, version string) (string, error) {
	tokens := strings.Fields(statusRaw)
	if len(tokens) != 3 {
		return "", fmt.Errorf("runtime package requires recovery: %s", statusRaw)
	}
	want, eflag, state := tokens[0], tokens[1], tokens[2]
	if strings.Contains(eflag, "reinstreq") {
		return "", fmt.Errorf("runtime package requires recovery: %s", statusRaw)
	}
	switch state {
	case "installed":
		if want == "install" || want == "hold" {
			return version, nil
		}
	case "not-installed", "config-files":
		return "", nil
	}
	return "", fmt.Errorf("runtime package requires recovery: %s", statusRaw)
}

func (m OpkgRuntimeManager) Version(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	statusPath := strings.TrimSpace(m.StatusPath)
	if statusPath == "" {
		statusPath = "/opt/lib/opkg/status"
	}
	data, err := os.ReadFile(statusPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, block := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok {
				fields[key] = strings.TrimSpace(value)
			}
		}
		if fields["Package"] == name {
			return parseOpkgStatusTuple(fields["Status"], fields["Version"])
		}
	}
	return "", nil
}

func (i *OnDemandInstaller) SetPackageManager(p RuntimePackageManager) { i.packages = p }
func (i *OnDemandInstaller) operationPath() string {
	return filepath.Join(i.layout.RootDir, "/opt/etc/awg-manager/firc-runtime-operation.json")
}

type runtimeOperation struct {
	Action          string `json:"action"`
	Pending         bool   `json:"pending"`
	PreviousVersion string `json:"previousVersion,omitempty"`
	TargetVersion   string `json:"targetVersion,omitempty"`
	ExternalPackage string `json:"externalPackage,omitempty"`
	Error           string `json:"error,omitempty"`
}

func (i *OnDemandInstaller) readOperation() (runtimeOperation, error) {
	var journal runtimeOperation
	b, err := os.ReadFile(i.operationPath())
	if os.IsNotExist(err) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	err = json.Unmarshal(b, &journal)
	return journal, err
}
func (i *OnDemandInstaller) saveOperation(j runtimeOperation) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return atomicWriteYAMLFile(i.operationPath(), b, 0600)
}

// API callers reserve before returning 202; competing requests cannot both be accepted.
func (i *OnDemandInstaller) reserve(action string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.busy {
		return ErrInstallBusy
	}
	i.busy = true
	i.busyAction = action
	i.lastError = ""
	i.progress = 0
	return nil
}
func (i *OnDemandInstaller) finish(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.busy = false
	i.busyAction = ""
	if err != nil {
		i.lastError = err.Error()
	} else {
		i.progress = 1
	}
}
func (i *OnDemandInstaller) StartInstall() error { return i.startAsync("install", nil, false, false) }
func (i *OnDemandInstaller) StartInstallWithOperator(op *Operator) error {
	return i.startAsync("install", op, false, false)
}
func (i *OnDemandInstaller) StartUninstall(op *Operator, selected, purge bool) error {
	return i.startAsync("uninstall", op, selected, purge)
}
func (i *OnDemandInstaller) startAsync(action string, op *Operator, selected, purge bool) error {
	if err := i.reserve(action); err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		i.finish(i.packageOperation(ctx, action, op, selected, purge))
	}()
	return nil
}

func (i *OnDemandInstaller) runtimeFilePaths() []string {
	return []string{
		i.layout.FircdBin,
		i.layout.TunvlessBin,
		i.rootPath("/opt/bin/fircd"),
		i.rootPath("/opt/bin/tunvless"),
		i.layout.InitScript,
		i.layout.NDMNetfilterHook,
		i.layout.NDMIfLayerHook,
	}
}

func lstatExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (i *OnDemandInstaller) hasCompleteUnmanagedRuntime() bool {
	fircdOK := presentRegular(i.layout.FircdBin) || presentRegular(i.rootPath("/opt/bin/fircd"))
	tunvlessOK := presentRegular(i.layout.TunvlessBin) || presentRegular(i.rootPath("/opt/bin/tunvless"))
	return fircdOK &&
		tunvlessOK &&
		presentRegular(i.layout.InitScript) &&
		presentRegular(i.layout.NDMNetfilterHook) &&
		lstatExists(i.layout.NDMIfLayerHook)
}

func (i *OnDemandInstaller) cleanupOrphanedRuntimeFiles() error {
	paths := append(i.runtimeFilePaths(), i.layout.ChecksumManifest)
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove orphaned FIRC runtime file %s: %w", p, err)
		}
	}
	return nil
}

func (i *OnDemandInstaller) isRuntimeMissingOrBroken(ctx context.Context, op *Operator) bool {
	if priorOp, jErr := i.readOperation(); jErr != nil || priorOp.Pending {
		return true
	}
	if op != nil && op.IsRecoveryRequired() {
		return true
	}
	if i.packages != nil {
		if ver, err := i.packages.Version(ctx, RuntimePackageName); err != nil || ver == "" {
			return true
		}
	}
	if _, err := os.Stat(i.layout.FircdBin); err != nil {
		if _, altErr := os.Stat(i.rootPath("/opt/bin/fircd")); altErr != nil {
			return true
		}
	}
	if _, err := os.Stat(i.layout.TunvlessBin); err != nil {
		if _, altErr := os.Stat(i.rootPath("/opt/bin/tunvless")); altErr != nil {
			return true
		}
	}
	if _, err := os.Stat(i.layout.InitScript); err != nil {
		return true
	}
	if _, err := os.Stat(i.layout.NDMNetfilterHook); err != nil {
		return true
	}
	if _, err := os.Lstat(i.layout.NDMIfLayerHook); err != nil {
		return true
	}
	if _, err := os.Stat(i.layout.ConfigFile); err != nil {
		return true
	}
	return false
}

func (i *OnDemandInstaller) verifyPreviousRuntimeBackup(cache, previousVersion string) (string, error) {
	var receipt FircBinarySpec
	b, err := os.ReadFile(filepath.Join(cache, "installed.json"))
	if err != nil {
		return "", fmt.Errorf("verified backup of installed runtime required before upgrade: %w", err)
	}
	if err := json.Unmarshal(b, &receipt); err != nil {
		return "", err
	}
	if !receipt.IsAvailable() {
		return "", errors.New("invalid installed runtime backup receipt")
	}
	previousIPK := filepath.Join(cache, "runtime-"+receipt.PackageSHA256+".ipk")
	if receipt.Version != previousVersion {
		return "", errors.New("installed runtime backup version mismatch")
	}
	if err := VerifyFileSHA256(previousIPK, receipt.PackageSHA256); err != nil {
		return "", err
	}
	if err := VerifyRuntimeIPK(previousIPK, i.arch, previousVersion); err != nil {
		return "", err
	}
	return previousIPK, nil
}

func (i *OnDemandInstaller) packageOperation(ctx context.Context, action string, op *Operator, selected, purge bool) (result error) {
	if action == "install" {
		i.prewarmVerifiedIPK(ctx)
	}
	if i.lockRuntime != nil {
		unlock := i.lockRuntime()
		defer unlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.engineActiveFn != nil {
		selected = i.engineActiveFn()
	}
	running := op != nil && op.IsRunning(ctx)
	if i.fircRunningFn != nil {
		running = i.fircRunningFn(ctx)
	}
	if running {
		return ErrFircRunning
	}
	if i.tunvlessRunningFn != nil && i.tunvlessRunningFn(ctx) {
		return ErrTunvlessRunning
	}
	missingOrBroken := action == "install" && i.isRuntimeMissingOrBroken(ctx, op)
	if selected && !missingOrBroken {
		return ErrFircActive
	}
	if !missingOrBroken && i.checkDependenciesFn != nil {
		deps, err := i.checkDependenciesFn(ctx)
		if err != nil {
			return fmt.Errorf("dependency check: %w", err)
		}
		if len(deps) > 0 {
			return fmt.Errorf("%w: %s", ErrFircDependencies, strings.Join(deps, ", "))
		}
	}
	if err := i.checkExternalPackageOwnership(); err != nil {
		return err
	}
	if i.packages == nil {
		return errors.New("firc: runtime package manager unavailable")
	}
	if checker, ok := i.packages.(interface{ CheckEnvironment() error }); ok {
		if err := checker.CheckEnvironment(); err != nil {
			return err
		}
	}
	if purge {
		return errors.New("firc: runtime removal preserves configuration; full data purge is not supported")
	}
	spec, fromFallback := i.installSpec()
	if action == "install" && !spec.IsAvailable() {
		return ErrReleaseUnavailable
	}
	priorOperation, journalErr := i.readOperation()
	if journalErr != nil {
		return fmt.Errorf("cannot read runtime recovery journal: %w", journalErr)
	}
	if priorOperation.Pending && priorOperation.Action == "migrate_external" {
		return fmt.Errorf("cannot start %q while %q migration requires recovery", action, priorOperation.Action)
	}
	if action == "install" {
		// До отметки Pending: после неё уборка сочла бы операцию незавершённой.
		i.pruneRuntimeCache(i.rootPath("/opt/var/cache/awg-manager/firc-runtime"))
	}
	if err := i.saveOperation(runtimeOperation{Action: action, Pending: true}); err != nil {
		return err
	}
	mutated := false
	defer func() {
		j := runtimeOperation{Action: action, Pending: result != nil && (mutated || priorOperation.Pending)}
		if result != nil {
			j.Error = result.Error()
		}
		if err := i.saveOperation(j); err != nil {
			result = errors.Join(result, fmt.Errorf("persist runtime operation result: %w", err))
		}
	}()
	if action == "uninstall" {
		mutated = true
		pkgVer, verErr := i.packages.Version(ctx, RuntimePackageName)
		if verErr != nil || pkgVer != "" {
			if err := i.packages.Remove(ctx, RuntimePackageName); err != nil {
				return err
			}
		}
		cleanupExtNetfilter(ctx, i.layout.RootDir)
		cleanupPoolRejectBarrierForRoot(ctx, i.layout.RootDir)
		version, err := i.packages.Version(ctx, RuntimePackageName)
		if err != nil {
			return err
		}
		if version != "" {
			return errors.New("runtime remains installed after removal")
		}
		if err := i.cleanupOrphanedRuntimeFiles(); err != nil {
			return err
		}
		for _, p := range i.runtimeFilePaths() {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				return fmt.Errorf("runtime file remains or cannot be checked: %s", p)
			}
		}
		return nil
	}
	required := spec.PackageSize*4 + (16 << 20)
	if i.freeDisk == nil {
		return errors.New("firc: free-space probe unavailable")
	}
	free, ok := i.freeDisk(filepath.Dir(i.layout.FircdBin))
	if !ok {
		return errors.New("firc: cannot verify free disk space")
	}
	if free < required {
		return ErrInsufficientDisk
	}
	cache := filepath.Join(i.layout.RootDir, "/opt/var/cache/awg-manager/firc-runtime")
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	previousVersion, verErr := i.packages.Version(ctx, RuntimePackageName)
	opkgStatusBroken := false
	if verErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		opkgStatusBroken = true
		previousVersion = ""
	}
	hadPreviousPackage := previousVersion != ""
	runtimeBroken := priorOperation.Pending || opkgStatusBroken || missingOrBroken
	previousIPK := ""
	if previousVersion != "" {
		var backupErr error
		previousIPK, backupErr = i.verifyPreviousRuntimeBackup(cache, previousVersion)
		if backupErr != nil {
			if !runtimeBroken {
				return backupErr
			}
			previousVersion = ""
			previousIPK = ""
		}
	} else if !priorOperation.Pending && !opkgStatusBroken && i.hasCompleteUnmanagedRuntime() {
		return errors.New("unmanaged FIRC runtime found; explicit ownership migration required")
	}
	stage, err := os.MkdirTemp(cache, "stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	ipk := filepath.Join(stage, "runtime.ipk")
	i.setProgress(.1)
	activeSpec, err := i.prepareVerifiedRuntimeIPK(ctx, cache, stage, ipk, spec, fromFallback)
	if err != nil {
		return err
	}
	payload, err := runtimePackageContents(ipk, i.arch, activeSpec.Version)
	if err != nil {
		return err
	}
	var unpacked int64
	for _, entry := range payload {
		unpacked += entry.header.Size
	}
	free, ok = i.freeDisk(cache)
	if !ok {
		return errors.New("cannot verify free space before package activation")
	}
	if free < unpacked+2*activeSpec.PackageSize+(16<<20) {
		return ErrInsufficientDisk
	}
	i.setProgress(.5)
	// Retain an authenticated artifact for offline repair if opkg is interrupted.
	data, err := os.ReadFile(ipk)
	if err != nil {
		return err
	}
	if err := persistRuntimeIPKCache(cache, data, spec, activeSpec, fromFallback); err != nil {
		return err
	}
	mutated = true
	if !hadPreviousPackage {
		if err := i.cleanupOrphanedRuntimeFiles(); err != nil {
			return err
		}
	}
	defer func() {
		if result == nil || previousVersion == "" {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := i.packages.Install(rollbackCtx, previousIPK); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime rollback failed: %w", err))
			return
		}
		v, err := i.packages.Version(rollbackCtx, RuntimePackageName)
		if err != nil || v != previousVersion {
			result = errors.Join(result, fmt.Errorf("runtime rollback version verification failed: %q: %v", v, err))
			return
		}
		if err := verifyInstalledRuntime(previousIPK, i.arch, previousVersion, i.layout); err != nil {
			result = errors.Join(result, fmt.Errorf("runtime rollback layout verification failed: %w", err))
			return
		}
		mutated = false
	}()
	if err := i.packages.Install(ctx, ipk); err != nil {
		return fmt.Errorf("opkg failed; verified reinstall required: %w", err)
	}
	i.setProgress(.85)
	version, err := i.packages.Version(ctx, RuntimePackageName)
	if err != nil {
		return err
	}
	if version != activeSpec.Version {
		return fmt.Errorf("installed runtime version %q, expected %q", version, activeSpec.Version)
	}
	if err := verifyInstalledRuntime(ipk, i.arch, activeSpec.Version, i.layout); err != nil {
		return fmt.Errorf("%w: %v", ErrLayoutIncomplete, err)
	}
	receipt, err := json.Marshal(activeSpec)
	if err != nil {
		return err
	}
	if err := atomicWriteYAMLFile(filepath.Join(cache, "installed.json"), receipt, 0600); err != nil {
		return err
	}
	return nil
}

type fallbackConvertedReceipt struct {
	SourceSHA256 string         `json:"sourceSha256"`
	RuntimeSpec  FircBinarySpec `json:"runtimeSpec"`
}

func downloadRuntimeArtifact(ctx context.Context, dl Downloader, spec FircBinarySpec, destPath string) error {
	if dl == nil {
		return ErrNoDownloader
	}
	urls := []string{spec.PackageURL}
	if validHTTPSURL(spec.MirrorURL) && spec.MirrorURL != spec.PackageURL {
		urls = append(urls, spec.MirrorURL)
	}
	var lastErr error
	for _, u := range urls {
		_ = os.Remove(destPath)
		if _, err := dl.DownloadFile(ctx, DownloadFileRequest{
			URL:          u,
			DestPath:     destPath,
			Timeout:      5 * time.Minute,
			MaxFileBytes: spec.PackageSize,
			Mode:         0600,
		}); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (i *OnDemandInstaller) prepareVerifiedRuntimeIPK(
	ctx context.Context,
	cache, stage, ipk string,
	spec FircBinarySpec,
	fromFallback bool,
) (FircBinarySpec, error) {
	verifiedCache := filepath.Join(cache, "verified.ipk")
	if !fromFallback {
		if VerifyFileSHA256(verifiedCache, spec.PackageSHA256) == nil {
			b, err := os.ReadFile(verifiedCache)
			if err != nil {
				return FircBinarySpec{}, err
			}
			if err := os.WriteFile(ipk, b, 0600); err != nil {
				return FircBinarySpec{}, err
			}
		} else {
			if err := downloadRuntimeArtifact(ctx, i.downloader, spec, ipk); err != nil {
				return FircBinarySpec{}, err
			}
		}
		if err := VerifyFileSHA256(ipk, spec.PackageSHA256); err != nil {
			return FircBinarySpec{}, fmt.Errorf("%w: %v", ErrSHA256Mismatch, err)
		}
		if err := VerifyRuntimeIPK(ipk, i.arch, spec.Version); err != nil {
			return FircBinarySpec{}, err
		}
		return spec, nil
	}

	fallbackReceiptPath := filepath.Join(cache, "fallback-converted.json")
	if rawRec, err := os.ReadFile(fallbackReceiptPath); err == nil {
		var rec fallbackConvertedReceipt
		if json.Unmarshal(rawRec, &rec) == nil &&
			rec.SourceSHA256 == spec.PackageSHA256 &&
			rec.RuntimeSpec.IsAvailable() &&
			rec.RuntimeSpec.Version == spec.Version &&
			VerifyFileSHA256(verifiedCache, rec.RuntimeSpec.PackageSHA256) == nil &&
			VerifyRuntimeIPK(verifiedCache, i.arch, rec.RuntimeSpec.Version) == nil {
			b, rErr := os.ReadFile(verifiedCache)
			if rErr != nil {
				return FircBinarySpec{}, rErr
			}
			if wErr := os.WriteFile(ipk, b, 0600); wErr != nil {
				return FircBinarySpec{}, wErr
			}
			return rec.RuntimeSpec, nil
		}
	}

	upstreamIPK := filepath.Join(stage, "upstream-firc.ipk")
	if err := downloadRuntimeArtifact(ctx, i.downloader, spec, upstreamIPK); err != nil {
		return FircBinarySpec{}, err
	}
	if err := VerifyFileSHA256(upstreamIPK, spec.PackageSHA256); err != nil {
		return FircBinarySpec{}, fmt.Errorf("%w: %v", ErrSHA256Mismatch, err)
	}
	if err := BuildRuntimeIPKFromEntwareIPK(upstreamIPK, i.arch, spec.Version, ipk); err != nil {
		return FircBinarySpec{}, err
	}
	runtimeSHA, err := ComputeFileSHA256(ipk)
	if err != nil {
		return FircBinarySpec{}, err
	}
	info, err := os.Stat(ipk)
	if err != nil {
		return FircBinarySpec{}, err
	}
	activeSpec := FircBinarySpec{
		Version:       spec.Version,
		PackageURL:    spec.PackageURL,
		MirrorURL:     spec.MirrorURL,
		PackageSHA256: runtimeSHA,
		PackageSize:   info.Size(),
	}
	if !activeSpec.IsAvailable() {
		return FircBinarySpec{}, errors.New("repackaged runtime IPK metadata invalid")
	}
	return activeSpec, nil
}

func persistRuntimeIPKCache(cache string, data []byte, sourceSpec, activeSpec FircBinarySpec, fromFallback bool) error {
	if err := atomicWriteYAMLFile(filepath.Join(cache, "verified.ipk"), data, 0600); err != nil {
		return err
	}
	if err := atomicWriteYAMLFile(filepath.Join(cache, "runtime-"+activeSpec.PackageSHA256+".ipk"), data, 0600); err != nil {
		return err
	}
	fallbackPath := filepath.Join(cache, "fallback-converted.json")
	if fromFallback {
		raw, err := json.Marshal(fallbackConvertedReceipt{
			SourceSHA256: sourceSpec.PackageSHA256,
			RuntimeSpec:  activeSpec,
		})
		if err != nil {
			return err
		}
		if err := atomicWriteYAMLFile(fallbackPath, raw, 0600); err != nil {
			return err
		}
	} else {
		_ = os.Remove(fallbackPath)
	}
	return nil
}

// pruneRuntimeCache убирает из кэша рантайма то, что осталось от прошлых
// операций: IPK прежних версий (каждый до 32 МиБ на /opt) и stage-каталоги
// оборванных операций. Хранится только IPK установленной версии — его ждёт
// откат следующего обновления. При незавершённой операции ничего не трогаем:
// её восстановлению нужен свой IPK. Зовётся под runtime-локом до отметки
// Pending текущей операции. Каталог кэша при удалении awg-manager не трогаем:
// рантайм FIRC — отдельный пакет и остаётся, а без installed.json и его IPK
// не обновится и не откатится.
func (i *OnDemandInstaller) pruneRuntimeCache(cache string) {
	if i.PendingOperationAction() != "" {
		return
	}
	keep := ""
	if b, err := os.ReadFile(filepath.Join(cache, "installed.json")); err == nil {
		var receipt FircBinarySpec
		if json.Unmarshal(b, &receipt) == nil {
			keep = "runtime-" + receipt.PackageSHA256 + ".ipk"
		}
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && (strings.HasPrefix(name, "stage-") || strings.HasPrefix(name, "upgrade-stage-") ||
			strings.HasPrefix(name, "ext-migrate-stage-") || strings.HasPrefix(name, ".firc-runtime-verify-")):
		case !e.IsDir() && strings.HasPrefix(name, "runtime-") && strings.HasSuffix(name, ".ipk") && name != keep:
		default:
			continue
		}
		_ = os.RemoveAll(filepath.Join(cache, name))
	}
}

// prewarmVerifiedIPK скачивает IPK рантайма в verified.ipk ДО runtime-лока:
// загрузка идёт до 10 минут, и держать на ней лок значило бы блокировать
// старт/стоп FIRC и правки маршрутизации. От параллельных операций установщика
// защищает reserve(). Под локом prepareVerifiedRuntimeIPK находит файл по SHA,
// проверяет его как обычно и не качает заново. Ошибка здесь не важна: путь
// под локом повторит загрузку и покажет её. Репак из Entware (fallback) не
// прогреваем — он и так идёт под локом.
func (i *OnDemandInstaller) prewarmVerifiedIPK(ctx context.Context) {
	spec, fromFallback := i.installSpec()
	if fromFallback || !spec.IsAvailable() {
		return
	}
	cache := i.rootPath("/opt/var/cache/awg-manager/firc-runtime")
	verified := filepath.Join(cache, "verified.ipk")
	if VerifyFileSHA256(verified, spec.PackageSHA256) == nil {
		return
	}
	if os.MkdirAll(cache, 0700) != nil {
		return
	}
	tmp := filepath.Join(cache, ".prewarm.ipk")
	defer os.Remove(tmp)
	if downloadRuntimeArtifact(ctx, i.downloader, spec, tmp) != nil {
		return
	}
	if VerifyFileSHA256(tmp, spec.PackageSHA256) == nil {
		_ = os.Rename(tmp, verified)
	}
}
