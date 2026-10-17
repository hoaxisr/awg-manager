package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
)

func main() {
	outPath := flag.String("out", "", "path to output TypeScript file (defaults to frontend/src/lib/types/mihomoRuleTypes.generated.ts)")
	flag.Parse()

	target := *outPath
	if target == "" {
		// Find repo root by looking for go.mod starting from current dir or executable dir
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to get current working directory: %v\n", err)
			os.Exit(1)
		}
		repoRoot := dir
		for {
			if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err == nil {
				break
			}
			parent := filepath.Dir(repoRoot)
			if parent == repoRoot {
				fmt.Fprintf(os.Stderr, "failed to locate repo root (go.mod) from %s\n", dir)
				os.Exit(1)
			}
			repoRoot = parent
		}
		target = filepath.Join(repoRoot, "frontend", "src", "lib", "types", "mihomoRuleTypes.generated.ts")
	}

	content := mihomo.RenderRuleTypesTypeScript()

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create directory for %s: %v\n", target, err)
		os.Exit(1)
	}

	// Write atomically using temporary file in the same directory
	tmpFile := fmt.Sprintf("%s.tmp.%d", target, os.Getpid())
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write temp file %s: %v\n", tmpFile, err)
		os.Exit(1)
	}

	if err := replaceFile(tmpFile, target); err != nil {
		fmt.Fprintf(os.Stderr, "failed to replace %s with %s: %v\n", target, tmpFile, err)
		_ = os.Remove(tmpFile)
		os.Exit(1)
	}

	fmt.Printf("Successfully generated %s (%d bytes)\n", target, len(content))
}
