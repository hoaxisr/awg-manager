package firc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (i *OnDemandInstaller) rootPath(absOrSlash string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(absOrSlash), "/")
	if i.layout.RootDir == "" || i.layout.RootDir == "/" {
		return "/" + rel
	}
	return filepath.Join(i.layout.RootDir, filepath.FromSlash(rel))
}

// RecoverPendingOperationOnStartup inspects firc-runtime-operation.json during
// manager startup before any automatic sing-box migration or FIRC daemon start.
// If a previous package operation was interrupted:
// install/uninstall are reconciled against the verified cache/receipt or fail closed.
func (i *OnDemandInstaller) RecoverPendingOperationOnStartup(ctx context.Context, op *Operator) error {
	if err := i.reserve("recover_startup"); err != nil {
		return err
	}
	var err error
	func() {
		if i.lockRuntime != nil {
			unlock := i.lockRuntime()
			defer unlock()
		} else if op != nil {
			unlock := op.LockRuntimeMutation()
			defer unlock()
		}
		err = i.recoverPendingLocked(ctx, op)
	}()
	i.finish(err)
	return err
}

func (i *OnDemandInstaller) recoverPendingLocked(ctx context.Context, op *Operator) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Сторонний firc: наш пакет поверх него не ставим и не снимаем, журнал ждёт.
	if err := i.checkNotForeign(); err != nil {
		return err
	}
	priorOp, journalErr := i.readOperation()
	if journalErr != nil {
		return fmt.Errorf("corrupt runtime operation journal (recovery_required): %w", journalErr)
	}
	if !priorOp.Pending {
		return nil
	}

	switch priorOp.Action {
	case "uninstall":
		if i.packages == nil {
			return errors.New("runtime package manager unavailable for pending uninstall recovery")
		}
		if ver, verErr := i.packages.Version(ctx, RuntimePackageName); verErr != nil || ver != "" {
			if rmErr := i.packages.Remove(ctx, RuntimePackageName); rmErr != nil {
				return fmt.Errorf("complete interrupted runtime uninstall on startup: %w", rmErr)
			}
		}
		cleanupExtNetfilter(ctx, i.layout.RootDir)
		cleanupPoolRejectBarrierForRoot(ctx, i.layout.RootDir)
		ver, verErr := i.packages.Version(ctx, RuntimePackageName)
		if verErr != nil || ver != "" {
			return fmt.Errorf("runtime remains installed after startup uninstall recovery (ver=%q, err=%v)", ver, verErr)
		}
		for _, p := range i.runtimeFilePaths() {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				return fmt.Errorf("runtime file remains after startup uninstall recovery: %s", p)
			}
		}
		return i.saveOperation(runtimeOperation{Action: "uninstall", Pending: false})

	case "install", "upgrade":
		if i.packages == nil {
			return errors.New("runtime package manager unavailable for pending install recovery")
		}
		cache := i.rootPath("/opt/var/cache/awg-manager/firc-runtime")
		receiptBytes, rErr := os.ReadFile(filepath.Join(cache, "installed.json"))
		if rErr == nil {
			var receipt FircBinarySpec
			if json.Unmarshal(receiptBytes, &receipt) == nil && receipt.IsAvailable() {
				cachedIPK := filepath.Join(cache, "runtime-"+receipt.PackageSHA256+".ipk")
				if VerifyFileSHA256(cachedIPK, receipt.PackageSHA256) == nil && VerifyRuntimeIPK(cachedIPK, i.arch, receipt.Version) == nil {
					ver, verErr := i.packages.Version(ctx, RuntimePackageName)
					if verErr != nil || ver != receipt.Version || verifyInstalledRuntime(cachedIPK, i.arch, receipt.Version, i.layout) != nil {
						if instErr := i.packages.Install(ctx, cachedIPK); instErr != nil {
							return fmt.Errorf("reinstall verified runtime package during startup recovery: %w", instErr)
						}
						ver, verErr = i.packages.Version(ctx, RuntimePackageName)
					}
					if verErr == nil && ver == receipt.Version && verifyInstalledRuntime(cachedIPK, i.arch, receipt.Version, i.layout) == nil {
						return i.saveOperation(runtimeOperation{Action: priorOp.Action, Pending: false})
					}
				}
			}
		}
		return errors.New("interrupted runtime install requires recovery before starting AWGM Route")

	case AbandonedExternalMigration:
		// Переезда с чужого пакета больше нет, а журнал сборок develop мог
		// остаться незавершённым. Прерванную на середине установку рантайма
		// чинит обычное «Восстановить», остальное — не повод держать автостарт.
		ver := ""
		if i.packages != nil {
			ver, _ = i.packages.Version(ctx, RuntimePackageName)
		}
		if _, err := os.Lstat(i.layout.FircdBin); err == nil && ver == "" {
			if err := i.saveOperation(runtimeOperation{Action: "install", Pending: true}); err != nil {
				return err
			}
			return errors.New("interrupted runtime install requires recovery before starting AWGM Route")
		}
		return i.saveOperation(runtimeOperation{Action: AbandonedExternalMigration, Pending: false})

	default:
		return fmt.Errorf("unknown pending runtime operation %q requires recovery", priorOp.Action)
	}
}

// AbandonedExternalMigration — операция переезда с пакета firc из сборок develop.
const AbandonedExternalMigration = "migrate_external"

// ExternalMigrationBackupDir — копия настроек, которую оставил тот переезд. Сам
// каталог не трогаем: в нём может быть единственный экземпляр настроек.
func (i *OnDemandInstaller) ExternalMigrationBackupDir() string {
	return i.rootPath("/opt/var/cache/awg-manager/firc-runtime/external-migration-backup")
}

func (i *OnDemandInstaller) isCurrentlyRunning(ctx context.Context, op *Operator) bool {
	if i.fircRunningFn != nil {
		return i.fircRunningFn(ctx)
	}
	if op != nil {
		return op.IsRunning(ctx)
	}
	return false
}

func (i *OnDemandInstaller) isTunvlessCurrentlyRunning(ctx context.Context) bool {
	if i.tunvlessRunningFn != nil {
		return i.tunvlessRunningFn(ctx)
	}
	return false
}
