// The check-git-accidental-large-file command compares two git tree-ishes and
// reports any blobs in the second tree that are new or changed and exceed a
// size threshold. It ignores deletions and submodules.
//
// It is intended to be used as a GitHub Action to catch accidental large file
// additions in pull requests. See action.yml.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

var maxSize = flag.Int64("max-size", 1_000_000, "maximum blob size in bytes")

func main() {
	log.SetPrefix("check-large-files: ")
	log.SetFlags(0)
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "usage: check-git-accidental-large-file [--max-size=N] <before> <after>\n")
		os.Exit(2)
	}

	beforeTree := resolveTree(flag.Arg(0))
	afterTree := resolveTree(flag.Arg(1))

	large := appendLargeAdditions(nil, beforeTree, afterTree, "", *maxSize)

	if len(large) == 0 {
		return
	}
	for _, f := range large {
		fmt.Printf("%s: %d bytes (%0.1f MiB)\n", f.path, f.size, float64(f.size)/(1<<20))
	}
	os.Exit(1)
}

// resolveTree resolves a git ref to its tree hash.
func resolveTree(ref string) string {
	out, err := exec.Command("git", "rev-parse", ref+"^{tree}").Output()
	if err != nil {
		log.Fatalf("resolving %s^{tree}: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

// treeEntry is a single entry from git ls-tree.
type treeEntry struct {
	mode string
	typ  string // "blob", "tree", or "commit"
	hash string
	size int64 // -1 for non-blob entries
	name string
}

// lsTree returns the entries of the given tree object.
func lsTree(treeHash string) []treeEntry {
	out, err := exec.Command("git", "ls-tree", "-z", "--long", treeHash).Output()
	if err != nil {
		log.Fatalf("git ls-tree %s: %v", treeHash, err)
	}
	var entries []treeEntry
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		// Format: "<mode> <type> <hash> <size>\t<name>"
		tab := bytes.IndexByte(record, '\t')
		if tab == -1 {
			continue
		}
		meta := strings.Fields(string(record[:tab]))
		if len(meta) != 4 {
			continue
		}
		var size int64 = -1
		if meta[3] != "-" {
			size, _ = strconv.ParseInt(meta[3], 10, 64)
		}
		entries = append(entries, treeEntry{
			mode: meta[0],
			typ:  meta[1],
			hash: meta[2],
			size: size,
			name: string(record[tab+1:]),
		})
	}
	return entries
}

type largeFile struct {
	path string
	size int64
}

// appendLargeAdditions walks two trees and returns dst plus any new or changed
// blobs exceeding maxSize. If beforeHash is empty, all blobs in afterHash are
// considered new.
func appendLargeAdditions(dst []largeFile, beforeHash, afterHash, prefix string, maxSize int64) []largeFile {
	afterEntries := lsTree(afterHash)

	var beforeByName map[string]treeEntry
	if beforeHash != "" {
		beforeEntries := lsTree(beforeHash)
		beforeByName = make(map[string]treeEntry, len(beforeEntries))
		for _, e := range beforeEntries {
			beforeByName[e.name] = e
		}
	}

	for _, ae := range afterEntries {
		if ae.mode == "160000" {
			continue // skip submodules
		}

		be, inBefore := beforeByName[ae.name]

		switch ae.typ {
		case "tree":
			if inBefore && be.hash == ae.hash {
				continue // subtree unchanged
			}
			var beforeSub string
			if inBefore && be.typ == "tree" {
				beforeSub = be.hash
			}
			dst = appendLargeAdditions(dst, beforeSub, ae.hash, prefix+ae.name+"/", maxSize)
		case "blob":
			if inBefore && be.hash == ae.hash {
				continue // blob unchanged
			}
			if ae.size > maxSize {
				dst = append(dst, largeFile{path: prefix + ae.name, size: ae.size})
			}
		}
	}
	return dst
}
