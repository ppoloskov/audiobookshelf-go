package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"audiobookshelf-compat/tests/replacement/harness"
)

func repositoryRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// digestEntry is one measured file, or one node_modules/.bin link with its target.
type digestEntry struct{ path, link string }

// digestPaths hashes the names and contents of every file under the given paths.
// Symlinks are refused, except entries of a node_modules/.bin directory that
// point inside that node_modules: they are hashed by their target path (the
// target file is measured as part of node_modules itself).
func digestPaths(root string, paths ...string) (string, error) {
	var entries []digestEntry
	for _, name := range paths {
		path := filepath.Join(root, name)
		err := filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				link, err := localBinLink(path)
				if err != nil {
					return err
				}
				entries = append(entries, digestEntry{path: path, link: link})
				return nil
			}
			// Finder metadata is not a verification input.
			if !entry.IsDir() && entry.Name() != ".DS_Store" {
				entries = append(entries, digestEntry{path: path})
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("inventory digest input %s: %w", name, err)
		}
	}
	slices.SortFunc(entries, func(a, b digestEntry) int { return strings.Compare(a.path, b.path) })
	hash := sha256.New()
	for _, entry := range entries {
		rel, err := filepath.Rel(root, entry.path)
		if err != nil {
			return "", err
		}
		if _, err := io.WriteString(hash, filepath.ToSlash(rel)+"\x00"); err != nil {
			return "", err
		}
		if entry.link != "" {
			if _, err := io.WriteString(hash, "link:"+entry.link); err != nil {
				return "", err
			}
		} else if err := hashFile(hash, entry.path); err != nil {
			return "", err
		}
		if _, err := hash.Write([]byte{0}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashFile(hash io.Writer, path string) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(hash, input)
	closeErr := input.Close()
	return errors.Join(copyErr, closeErr)
}

// localBinLink returns the slash-separated target (relative to its node_modules)
// of a symlink directly inside node_modules/.bin. Any other symlink, and any
// link leaving node_modules, is an error: its code would not be measured.
func localBinLink(path string) (string, error) {
	bin := filepath.Dir(path)
	modules := filepath.Dir(bin)
	if filepath.Base(bin) != ".bin" || filepath.Base(modules) != "node_modules" {
		return "", fmt.Errorf("unexpected symlink in verification input: %s", path)
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(bin, target)
	}
	rel, err := filepath.Rel(modules, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unexpected symlink in verification input: %s links outside node_modules", path)
	}
	return filepath.ToSlash(rel), nil
}

func inputDigests(root, node string) (inputs, error) {
	cmd := exec.Command("git", "diff", "--exit-code", sourcePin, "--", "server", "client", "test", "index.js", "package.json", "package-lock.json", "tsconfig.server.json")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		return inputs{}, fmt.Errorf("upstream source/client/build inputs differ from pinned v2.37.2: %w", err)
	}
	untracked := exec.Command("git", "ls-files", "--others", "--exclude-standard", "--", "server", "client", "test", "index.js")
	untracked.Dir = root
	raw, err := untracked.Output()
	if err != nil {
		return inputs{}, err
	}
	if len(raw) != 0 {
		return inputs{}, fmt.Errorf("untracked upstream input is not pinned")
	}
	var result inputs
	for _, item := range []struct {
		dest  *string
		paths []string
	}{
		{&result.Source, []string{"server", "test", "index.js", "client/dist", "dist-server/server", "dist-server/test", "dist-server/index.js"}},
		{&result.Requirements, []string{"docs/agent-work/capabilities", "docs/agent-work/CONTRACT.md", "go-backend/ARCHITECTURE.md", "go-backend/contracts/clients", "go-backend/contracts/client-usage.json", "go-backend/contracts/mobile-client-audit.md"}},
		{&result.Scenarios, []string{"go-backend/tests/replacement"}},
		{&result.Fixture, []string{"go-backend/internal/contracttest/testdata/install"}},
		{&result.Machine, []string{"go-backend/tools/verify", "go-backend/tools/source-inventory.mjs", "go-backend/tools/replacement-contract-map.mjs", "go-backend/tools/parity"}},
		{&result.GoSource, []string{"go-backend/go.mod", "go-backend/go.sum", "go-backend/internal", "go-backend/cmd"}},
		{&result.DependencyInstall, []string{"package-lock.json", "node_modules"}},
	} {
		value, err := digestPaths(root, item.paths...)
		if err != nil {
			return inputs{}, err
		}
		*item.dest = value
	}
	nodePath, err := exec.LookPath(node)
	if err != nil {
		return inputs{}, err
	}
	nodePath, err = filepath.EvalSymlinks(nodePath)
	if err != nil {
		return inputs{}, err
	}
	result.NodeRuntime, err = digestPaths(filepath.Dir(nodePath), filepath.Base(nodePath))
	if err != nil {
		return inputs{}, err
	}
	return result, nil
}

func loadCapabilities(root string) ([]capability, error) {
	paths, err := filepath.Glob(filepath.Join(root, "docs/agent-work/capabilities/*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("zero canonical requirements")
	}
	var records []capability
	for _, path := range paths {
		raw, err := readMetadata(path)
		if err != nil {
			return nil, err
		}
		var record capability
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		record.Path = path
		records = append(records, record)
	}
	return records, nil
}

// loadPackets reads packets.json: packet name to the requirement ids it owns.
func loadPackets(root string) (map[string][]string, error) {
	raw, err := readMetadata(filepath.Join(root, "go-backend/tests/replacement/packets.json"))
	if err != nil {
		return nil, err
	}
	var packets map[string][]string
	if err := json.Unmarshal(raw, &packets); err != nil {
		return nil, fmt.Errorf("decode packets.json: %w", err)
	}
	if len(packets) == 0 {
		return nil, fmt.Errorf("packets.json lists no packets")
	}
	return packets, nil
}

// validatePlan checks the declared scenarios against the capability records and
// packets.json: known packet, requirement owned by that packet, known invariant,
// applicable case and state, and one assertion name per packet.
func validatePlan(records []capability, packets map[string][]string, plan []harness.Declaration) error {
	if len(plan) == 0 {
		return fmt.Errorf("zero shared assertion bindings")
	}
	ids := make(map[string]capability)
	for _, c := range records {
		// The two approved exclusions are already outside these records.
		// Changing the scope of an existing requirement cannot grant coverage.
		if c.Scope != "in" || c.ID == "" || len(c.Invariants) == 0 {
			return fmt.Errorf("invalid canonical scope, ID or empty invariants on requirement %q", c.ID)
		}
		if _, exists := ids[c.ID]; exists {
			return fmt.Errorf("duplicate canonical requirement %s", c.ID)
		}
		ids[c.ID] = c
	}
	seen := make(map[string]bool)
	for _, d := range plan {
		owned, ok := packets[d.Packet]
		if !ok {
			return fmt.Errorf("packet %q is not a key of packets.json", d.Packet)
		}
		if !slices.Contains(owned, d.Requirement) {
			return fmt.Errorf("%s/%s: requirement %s does not belong to this packet in packets.json", d.Packet, d.Assertion, d.Requirement)
		}
		c, ok := ids[d.Requirement]
		if !ok || c.Scope != "in" {
			return fmt.Errorf("%s/%s refers to unknown/out-of-scope requirement", d.Packet, d.Assertion)
		}
		if !slices.ContainsFunc(c.Invariants, func(i invariant) bool { return i.ID == d.Invariant }) {
			return fmt.Errorf("%s/%s refers to unknown invariant", d.Packet, d.Assertion)
		}
		if _, ok := c.Cases[d.Case]; !ok || c.Cases[d.Case] == "not_applicable" {
			return fmt.Errorf("%s/%s refers to an invalid case", d.Packet, d.Assertion)
		}
		if c.Stateful {
			if _, ok := c.StateScenarios[d.State]; !ok || c.StateScenarios[d.State] == "not_applicable" {
				return fmt.Errorf("%s/%s refers to invalid state", d.Packet, d.Assertion)
			}
		} else if d.State != "stateless" {
			return fmt.Errorf("stateless assertion %s/%s has a lifecycle state", d.Packet, d.Assertion)
		}
		key := d.Packet + "/" + d.Assertion
		if d.Assertion == "" || seen[key] {
			return fmt.Errorf("duplicate/empty assertion name %s", key)
		}
		seen[key] = true
	}
	return nil
}

const maxMetadataBytes int64 = 16 << 20

func readMetadata(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() // read-only metadata has no buffered writes to flush
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() > maxMetadataBytes {
		return nil, fmt.Errorf("verification metadata exceeds %d bytes", maxMetadataBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxMetadataBytes {
		return nil, fmt.Errorf("verification metadata grew beyond size limit")
	}
	return raw, nil
}
