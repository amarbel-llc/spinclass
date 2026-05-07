// POC: pre-filter the worktree's gc closure to only-dead paths before
// calling `nix-store --delete`, sidestepping the fail-fast that bites
// when a path in our closure is also rooted externally.
//
// Hardcoded constants below. Everything happens in main(). No flags,
// no env, no config, no helper packages.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const worktreePath = "/home/sasha/eng/repos/dodder/.worktrees/fast-linden"

func main() {
	abs, err := filepath.Abs(worktreePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: abs(%q): %v\n", worktreePath, err)
		os.Exit(1)
	}
	fmt.Printf("worktree: %s\n", abs)

	// === enumerate gcroots, partition into ours vs external ===

	rootsOut, err := exec.Command("nix-store", "--gc", "--print-roots").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: nix-store --gc --print-roots: %v\n", err)
		os.Exit(1)
	}

	var ourRootStorePaths, externalRoots []string
	var ourRootLinkPaths []string // user-side symlinks to rm during simulate-close
	totalRoots := 0
	sc := bufio.NewScanner(bytes.NewReader(rootsOut))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.Contains(line, "{") {
			continue
		}
		idx := strings.Index(line, " -> ")
		if idx < 0 {
			continue
		}
		link := strings.TrimSpace(line[:idx])
		store := strings.TrimSpace(line[idx+len(" -> "):])
		if link == "" || store == "" {
			continue
		}
		totalRoots++

		// Resolve link to determine if it lands in the worktree.
		// Mirror parseRoots: check link path or one Readlink hop.
		resolved := link
		inWT := func(p string) bool {
			if p == abs {
				return true
			}
			return strings.HasPrefix(p, abs+string(filepath.Separator))
		}
		if !inWT(resolved) {
			t, lerr := os.Readlink(link)
			if lerr == nil {
				if !filepath.IsAbs(t) {
					t = filepath.Join(filepath.Dir(link), t)
				}
				resolved = filepath.Clean(t)
			}
		}
		if inWT(resolved) {
			ourRootStorePaths = append(ourRootStorePaths, store)
			ourRootLinkPaths = append(ourRootLinkPaths, link)
		} else {
			externalRoots = append(externalRoots, store)
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: scanning --print-roots output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("total_roots: %d\n", totalRoots)
	fmt.Printf("our_roots: %d\n", len(ourRootStorePaths))
	fmt.Printf("external_roots: %d\n", len(externalRoots))

	if len(ourRootStorePaths) == 0 {
		fmt.Println("PASS: no gcroots resolve into worktree; nothing to test")
		return
	}

	// === expand our closure ===

	ourArgs := append([]string{"--query", "--requisites"}, ourRootStorePaths...)
	ourClosureOut, err := exec.Command("nix-store", ourArgs...).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: nix-store --query --requisites <our roots>: %v\n", err)
		os.Exit(1)
	}
	var ourClosure []string
	seen := make(map[string]bool)
	sc = bufio.NewScanner(bytes.NewReader(ourClosureOut))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		p := strings.TrimSpace(sc.Text())
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		ourClosure = append(ourClosure, p)
	}
	fmt.Printf("our_closure: %d paths\n", len(ourClosure))

	// === expand external alive set ===

	aliveSet := make(map[string]bool)
	if len(externalRoots) > 0 {
		extArgs := append([]string{"--query", "--requisites"}, externalRoots...)
		extOut, err := exec.Command("nix-store", extArgs...).Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: nix-store --query --requisites <external roots>: %v\n", err)
			os.Exit(1)
		}
		sc = bufio.NewScanner(bytes.NewReader(extOut))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			p := strings.TrimSpace(sc.Text())
			if p != "" {
				aliveSet[p] = true
			}
		}
	}
	fmt.Printf("external_alive: %d paths\n", len(aliveSet))

	// === filter ===

	var deletable []string
	keptByExternal := 0
	for _, p := range ourClosure {
		if aliveSet[p] {
			keptByExternal++
			continue
		}
		deletable = append(deletable, p)
	}
	fmt.Printf("kept_by_external: %d paths\n", keptByExternal)
	fmt.Printf("deletable: %d paths\n", len(deletable))
	overlapPct := 0.0
	if len(ourClosure) > 0 {
		overlapPct = float64(keptByExternal) * 100 / float64(len(ourClosure))
	}
	fmt.Printf("overlap: %.1f%%\n", overlapPct)

	if len(deletable) == 0 {
		fmt.Println("PASS: deletable set empty; whole closure is held by external roots")
		return
	}

	// === simulate worktree close: rm our_roots' user-side symlinks ===
	// Production close.go: planNixGC captures the plan, then
	// git.WorktreeForceRemove deletes the worktree dir. The auto-roots
	// in /nix/var/nix/gcroots/auto/* become dangling, and `nix-store
	// --delete` auto-prunes them before processing the closure list.
	// We mimic that here by rm'ing each user-side gcroot symlink.
	fmt.Println()
	fmt.Println("=== simulating worktree close (rm our_roots link paths) ===")
	for _, lp := range ourRootLinkPaths {
		if rmErr := os.Remove(lp); rmErr != nil && !os.IsNotExist(rmErr) {
			fmt.Printf("  warn: rm %s: %v\n", lp, rmErr)
			continue
		}
		fmt.Printf("  rm %s\n", lp)
	}

	// === actually delete ===

	fmt.Println()
	fmt.Println("=== running nix-store --delete on deletable set ===")
	delArgs := append([]string{"--delete"}, deletable...)
	cmd := exec.Command("nix-store", delArgs...)
	var captured bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &captured)
	cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	runErr := cmd.Run()

	// === classify ===

	out := captured.String()
	deletedCount := strings.Count(out, "deleting '")
	cannotCount := strings.Count(out, "Cannot delete path") +
		strings.Count(out, "cannot delete path")
	stillAlive := strings.Count(strings.ToLower(out), "still alive") +
		strings.Count(strings.ToLower(out), "still in use") +
		strings.Count(strings.ToLower(out), "is in use") +
		strings.Count(strings.ToLower(out), "referenced by")

	fmt.Println()
	fmt.Printf("exit: %v\n", runErr)
	fmt.Printf("deleted_count_in_output: %d\n", deletedCount)
	fmt.Printf("cannot_delete_lines: %d\n", cannotCount)
	fmt.Printf("still_alive_or_referenced: %d\n", stillAlive)
	fmt.Printf("expected_deletable: %d\n", len(deletable))

	pass := runErr == nil && cannotCount == 0 && stillAlive == 0
	if pass {
		fmt.Println("PASS")
		os.Exit(0)
	}
	fmt.Println("FAIL")
	os.Exit(1)
}
