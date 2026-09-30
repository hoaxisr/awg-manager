package mihomo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

// GenerationStoreHooks allows injecting failpoints during generation bundle publication.
type GenerationStoreHooks struct {
	FailFileFsync                 bool
	FailStagingDirFsync           bool
	FailRename                    bool
	FailParentDirFsync            bool
	FailPointerWrite              bool
	FailPointerFsync              bool
	FailPreexistingStagingRemoval bool
	FailStagingRemoval            bool
	FailCandidateDirRemoval       bool
}

// GenerationStore manages immutable generation bundles and the LKG pointer.
type GenerationStore struct {
	baseDir        string
	generationsDir string
	lkgPointerFile string
	hooks          GenerationStoreHooks
}

// NewGenerationStore creates a GenerationStore rooted at baseDir.
func NewGenerationStore(baseDir string) *GenerationStore {
	return &GenerationStore{
		baseDir:        baseDir,
		generationsDir: filepath.Join(baseDir, "generations"),
		lkgPointerFile: filepath.Join(baseDir, "lkg.pointer.json"),
	}
}

// SetHooks sets failpoint injection hooks for testing.
func (s *GenerationStore) SetHooks(hooks GenerationStoreHooks) {
	s.hooks = hooks
}

// GenerationsDir returns the directory containing archived generation bundles.
func (s *GenerationStore) GenerationsDir() string {
	return s.generationsDir
}

// LKGPointerFile returns the path to lkg.pointer.json.
func (s *GenerationStore) LKGPointerFile() string {
	return s.lkgPointerFile
}

// PublishStagedBundle creates an immutable generation bundle and advances the LKG pointer.
func (s *GenerationStore) PublishStagedBundle(
	genID string,
	genNum uint64,
	configBytes []byte,
	storeSnapshotFile string,
	appliedRec AppliedGenerationRecord,
	epoch string,
) error {
	if err := ValidateBasename(genID); err != nil {
		return fmt.Errorf("invalid generation ID %q: %w", genID, err)
	}
	if err := appliedRec.ValidateSchema(); err != nil {
		return fmt.Errorf("invalid applied generation record: %w", err)
	}

	// 1. Validate active config digest
	if appliedRec.RuntimeMode != RuntimeOff {
		expectedConfigDigest := strictfs.ComputeBytesDigest(configBytes)
		if appliedRec.AppliedConfigDigest != expectedConfigDigest {
			return fmt.Errorf("config digest mismatch: applied=%s, actual=%s",
				appliedRec.AppliedConfigDigest, expectedConfigDigest)
		}
	}

	// 2. Validate pre-mutation store snapshot digest
	if storeSnapshotFile != "" {
		if err := AssertPathConfined(storeSnapshotFile, s.baseDir); err != nil {
			return fmt.Errorf("store snapshot out of bounds: %w", err)
		}
		actualStoreDigest, err := strictfs.ComputeFileDigest(storeSnapshotFile)
		if err != nil {
			return fmt.Errorf("compute store snapshot digest: %w", err)
		}
		if appliedRec.AppliedStoreDigest != actualStoreDigest {
			return fmt.Errorf("store snapshot digest mismatch: applied=%s, actual=%s",
				appliedRec.AppliedStoreDigest, actualStoreDigest)
		}
	}

	// 3. Ensure generations directory exists with 0700 permissions
	if err := os.MkdirAll(s.generationsDir, 0700); err != nil {
		return fmt.Errorf("create generations directory: %w", err)
	}

	secureGens, err := strictfs.NewSecureDir(s.generationsDir)
	if err != nil {
		return fmt.Errorf("secure generations directory: %w", err)
	}

	// 4. Clean up any pre-existing staging directory generations/.tmp.<genID>
	stagingName := ".tmp." + genID
	stagingDir := filepath.Join(s.generationsDir, stagingName)
	if s.hooks.FailPreexistingStagingRemoval {
		return errors.New("failpoint: pre-existing staging directory removal failed")
	}
	if err := secureGens.RemoveAll(stagingName); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("pre-clean staging directory %s: %w", stagingName, err)
	}
	if _, err := os.Stat(stagingDir); err == nil {
		return fmt.Errorf("verify staging directory removed %s: directory still exists", stagingName)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat staging directory %s: %w", stagingName, err)
	}

	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer func() {
		// Clean up staging dir if publication did not rename it
		if !s.hooks.FailStagingRemoval {
			_ = secureGens.RemoveAll(stagingName)
		}
	}()

	secureStaging, err := strictfs.NewSecureDir(stagingDir)
	if err != nil {
		return fmt.Errorf("secure staging directory: %w", err)
	}

	// 5. Write config.yaml
	if s.hooks.FailFileFsync {
		return errors.New("failpoint: file fsync failed")
	}
	if appliedRec.RuntimeMode != RuntimeOff {
		if err := secureStaging.WriteFileAtomic("config.yaml", configBytes, 0600); err != nil {
			return fmt.Errorf("write bundle config.yaml: %w", err)
		}
	}

	// 6. Copy store.snapshot.json if present
	if storeSnapshotFile != "" {
		if s.hooks.FailFileFsync {
			return errors.New("failpoint: file fsync failed")
		}
		storeData, err := os.ReadFile(storeSnapshotFile)
		if err != nil {
			return fmt.Errorf("read store snapshot: %w", err)
		}
		if err := secureStaging.WriteFileAtomic("store.snapshot.json", storeData, 0600); err != nil {
			return fmt.Errorf("copy store snapshot to bundle: %w", err)
		}
	}

	// 7. Write generation.manifest.json
	bridgeIdentVer := appliedRec.BridgeIdentityVersion
	if bridgeIdentVer == 0 {
		bridgeIdentVer = CurrentBridgeIdentityVersion
	}
	appliedBridgesDigest := appliedRec.AppliedBridgesDigest
	if appliedBridgesDigest == "" && len(appliedRec.AppliedBridges) > 0 {
		appliedBridgesDigest = BridgesDigest(appliedRec.AppliedBridges)
	}
	archivedAt := appliedRec.AppliedAt
	if archivedAt.IsZero() {
		archivedAt = time.Now()
	}
	gm := GenerationManifest{
		Version:                   1,
		BridgeIdentityVersion:     bridgeIdentVer,
		GenerationID:              genID,
		GenerationNumber:          genNum,
		ArchivedAt:                archivedAt,
		AppliedStoreDigest:        appliedRec.AppliedStoreDigest,
		AppliedDesiredStoreDigest: appliedRec.AppliedDesiredStoreDigest,
		AppliedConfigDigest:       appliedRec.AppliedConfigDigest,
		AppliedInputDigest:        appliedRec.AppliedInputDigest,
		AppliedListeners:          appliedRec.AppliedListeners,
		AppliedBridges:            appliedRec.AppliedBridges,
		AppliedBridgesDigest:      appliedBridgesDigest,
		RuntimeMode:               appliedRec.RuntimeMode,
	}
	manifestBytes, err := json.MarshalIndent(gm, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal generation manifest: %w", err)
	}
	if s.hooks.FailFileFsync {
		return errors.New("failpoint: file fsync failed")
	}
	if err := secureStaging.WriteFileAtomic("generation.manifest.json", manifestBytes, 0600); err != nil {
		return fmt.Errorf("write generation.manifest.json: %w", err)
	}

	// 8. Fsync staging directory
	if s.hooks.FailStagingDirFsync {
		return errors.New("failpoint: staging directory fsync failed")
	}
	if err := strictfs.FsyncDirectory(stagingDir); err != nil {
		return fmt.Errorf("fsync staging directory: %w", err)
	}

	// 9. Atomic rename staging directory to final directory
	if s.hooks.FailRename {
		return errors.New("failpoint: rename staging directory failed")
	}
	if err := secureGens.Rename(stagingName, genID); err != nil {
		return fmt.Errorf("rename staging directory to final bundle: %w", err)
	}

	// 10. Fsync generations/ directory
	if s.hooks.FailParentDirFsync {
		return errors.New("failpoint: parent directory fsync failed")
	}
	if err := strictfs.FsyncDirectory(s.generationsDir); err != nil {
		return fmt.Errorf("fsync generations directory: %w", err)
	}

	// 11. Fsync base directory
	if err := strictfs.FsyncDirectory(s.baseDir); err != nil {
		return fmt.Errorf("fsync base directory: %w", err)
	}

	return nil
}

// RemoveCandidateGeneration safely removes an uncommitted generation bundle directory.
// It verifies that genID is well-formed, non-empty, and matches neither activeGenID nor lkgGenID.
func (s *GenerationStore) RemoveCandidateGeneration(genID, activeGenID, lkgGenID string) error {
	if err := ValidateBasename(genID); err != nil {
		return fmt.Errorf("invalid generation ID %q: %w", genID, err)
	}
	if genID == "" {
		return nil
	}
	if genID == activeGenID || (lkgGenID != "" && genID == lkgGenID) {
		return fmt.Errorf("refusing to remove active or LKG generation %s", genID)
	}

	if _, err := os.Stat(s.generationsDir); os.IsNotExist(err) {
		return nil
	}

	targetDir := filepath.Join(s.generationsDir, genID)
	stagingName := ".tmp." + genID
	stagingDir := filepath.Join(s.generationsDir, stagingName)
	_, tErr := os.Stat(targetDir)
	_, sErr := os.Stat(stagingDir)
	if os.IsNotExist(tErr) && os.IsNotExist(sErr) {
		return nil
	}

	secureGens, err := strictfs.NewSecureDir(s.generationsDir)
	if err != nil {
		return fmt.Errorf("secure generations directory: %w", err)
	}
	defer secureGens.Close()

	if s.hooks.FailStagingRemoval {
		return errors.New("failpoint: staging directory removal failed")
	}

	// Remove lingering staging if present
	if !os.IsNotExist(sErr) {
		if err := secureGens.RemoveAll(stagingName); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove staging directory %s: %w", stagingName, err)
		}
		if _, err := os.Stat(stagingDir); err == nil {
			return fmt.Errorf("verify staging directory removed %s: still exists", stagingName)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("verify staging directory removed %s: %w", stagingName, err)
		}
	}

	if s.hooks.FailCandidateDirRemoval {
		return errors.New("failpoint: candidate directory removal failed")
	}

	if !os.IsNotExist(tErr) {
		if err := secureGens.RemoveAll(genID); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove generation directory %s: %w", genID, err)
		}

		if _, err := os.Stat(targetDir); err == nil {
			return fmt.Errorf("verify generation directory removed %s: still exists", genID)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("verify generation directory removed %s: %w", genID, err)
		}
	}

	if err := strictfs.FsyncDirectory(s.generationsDir); err != nil {
		return fmt.Errorf("fsync generations directory: %w", err)
	}

	return nil
}

// ReadLKGPointer loads and validates lkg.pointer.json.
func (s *GenerationStore) ReadLKGPointer() (*LKGPointer, error) {
	secureBase, err := strictfs.NewSecureDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("secure base dir: %w", err)
	}
	data, err := secureBase.ReadFile("lkg.pointer.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("read lkg.pointer.json: %w", err)
	}
	var ptr LKGPointer
	if err := DecodeJSONStrict(data, &ptr); err != nil {
		return nil, fmt.Errorf("unmarshal lkg pointer: %w", err)
	}
	if err := ptr.ValidateSchema(); err != nil {
		return nil, fmt.Errorf("validate lkg pointer schema: %w", err)
	}
	bundleDir := filepath.Join(s.generationsDir, ptr.GenerationID)
	if info, err := os.Stat(bundleDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("referenced generation directory %s missing or not a directory", bundleDir)
	}
	return &ptr, nil
}

// ReadGenerationBundle loads manifest and confirms files for a specific generation.
func (s *GenerationStore) ReadGenerationBundle(genID string) (*GenerationManifest, string, string, error) {
	if err := ValidateBasename(genID); err != nil {
		return nil, "", "", fmt.Errorf("invalid generation ID: %w", err)
	}
	bundleDir := filepath.Join(s.generationsDir, genID)
	if err := AssertPathConfined(bundleDir, s.generationsDir); err != nil {
		return nil, "", "", fmt.Errorf("bundle directory out of bounds: %w", err)
	}

	secureBundle, err := strictfs.NewSecureDir(bundleDir)
	if err != nil {
		return nil, "", "", fmt.Errorf("secure bundle dir: %w", err)
	}

	data, err := secureBundle.ReadFile("generation.manifest.json")
	if err != nil {
		return nil, "", "", fmt.Errorf("read generation.manifest.json: %w", err)
	}
	var gm GenerationManifest
	if err := DecodeJSONStrict(data, &gm); err != nil {
		return nil, "", "", fmt.Errorf("unmarshal generation manifest: %w", err)
	}
	if err := gm.ValidateSchema(); err != nil {
		return nil, "", "", fmt.Errorf("validate generation manifest schema: %w", err)
	}

	configPath := filepath.Join(bundleDir, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		if gm.RuntimeMode != RuntimeOff {
			return nil, "", "", fmt.Errorf("bundle config.yaml missing: %w", err)
		}
		configPath = "" // No config path for RuntimeOff
	}

	storeSnapshotPath := filepath.Join(bundleDir, "store.snapshot.json")
	if _, err := os.Stat(storeSnapshotPath); err != nil {
		storeSnapshotPath = "" // Optional if no store was snapshot
	}

	return &gm, configPath, storeSnapshotPath, nil
}

// RunRetentionGC cleans up unreferenced bundles while preserving active, LKG, and keepRecent youngest bundles.
func (s *GenerationStore) RunRetentionGC(activeGenID, lkgGenID string, protectedGenIDs []string, keepRecent int) error {
	entries, err := os.ReadDir(s.generationsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read generations directory: %w", err)
	}

	type genEntry struct {
		name    string
		modTime time.Time
	}
	var validGenerations []genEntry

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".tmp.") {
			// Stale temporary staging directory: safe to remove if old
			info, _ := e.Info()
			if info != nil && time.Since(info.ModTime()) > 5*time.Minute {
				secureGens, err := strictfs.NewSecureDir(s.generationsDir)
				if err == nil {
					if rmErr := secureGens.RemoveAll(name); rmErr != nil {
						// failed to clean stale temp directory, continue
					}
				}
			}
			continue
		}
		if err := ValidateBasename(name); err != nil {
			continue // ignore invalid names
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		validGenerations = append(validGenerations, genEntry{name: name, modTime: info.ModTime()})
	}

	// Sort newest first
	sort.Slice(validGenerations, func(i, j int) bool {
		return validGenerations[i].modTime.After(validGenerations[j].modTime)
	})

	protected := make(map[string]bool)
	if activeGenID != "" {
		protected[activeGenID] = true
	}
	if lkgGenID != "" {
		protected[lkgGenID] = true
	}
	for _, id := range protectedGenIDs {
		if id != "" {
			protected[id] = true
		}
	}
	// Protect keepRecent additional newest unreferenced generations
	count := 0
	for _, g := range validGenerations {
		if protected[g.name] {
			continue
		}
		protected[g.name] = true
		count++
		if count >= keepRecent {
			break
		}
	}

	for _, g := range validGenerations {
		if protected[g.name] {
			continue
		}
		secureGens, err := strictfs.NewSecureDir(s.generationsDir)
		if err == nil {
			if rmErr := secureGens.RemoveAll(g.name); rmErr != nil {
				return fmt.Errorf("remove stale generation %s: %w", g.name, rmErr)
			}
		}
	}

	if err := strictfs.FsyncDirectory(s.generationsDir); err != nil {
		return fmt.Errorf("fsync generations directory: %w", err)
	}
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// AdvanceLKGPointer updates the LKG pointer to the specified generation.
func (s *GenerationStore) AdvanceLKGPointer(
	genID string,
	genNum uint64,
	appliedRec AppliedGenerationRecord,
	epoch string,
) error {
	ptr := LKGPointer{
		Version:                   1,
		GenerationID:              genID,
		GenerationNumber:          genNum,
		AppliedConfigDigest:       appliedRec.AppliedConfigDigest,
		AppliedStoreDigest:        appliedRec.AppliedStoreDigest,
		AppliedDesiredStoreDigest: appliedRec.AppliedDesiredStoreDigest,
		UpdatedEpoch:              epoch,
		UpdatedAt:                 time.Now(),
	}
	ptrBytes, err := json.MarshalIndent(ptr, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lkg pointer: %w", err)
	}
	if s.hooks.FailPointerWrite {
		return errors.New("failpoint: pointer write failed")
	}
	if err := strictfs.StrictWriteAtomic(s.lkgPointerFile, ptrBytes, 0600); err != nil {
		return fmt.Errorf("write lkg.pointer.json: %w", err)
	}
	if s.hooks.FailPointerFsync {
		return errors.New("failpoint: pointer fsync failed")
	}
	if err := strictfs.FsyncFile(s.lkgPointerFile); err != nil {
		return fmt.Errorf("fsync lkg.pointer.json: %w", err)
	}
	if err := strictfs.FsyncDirectory(s.baseDir); err != nil {
		return fmt.Errorf("fsync base directory: %w", err)
	}
	return nil
}
