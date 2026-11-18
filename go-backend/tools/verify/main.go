// Command verify is the upstream-first replacement verification machine.
// It never treats a passing smoke suite or a source inventory as full coverage.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const sourcePin = "edb1f7efc92ecacdef51aafb4d9d5054ce10604f"

type inputs struct {
	Source            string `json:"source_and_assets"`
	Requirements      string `json:"requirements"`
	Scenarios         string `json:"scenarios"`
	Fixture           string `json:"starting_fixture"`
	Machine           string `json:"machine"`
	GoSource          string `json:"go_source_and_modules"`
	NodeRuntime       string `json:"node_executable"`
	DependencyInstall string `json:"installed_node_dependencies"`
}
type targetResult struct {
	Status        string                   `json:"status"`
	Error         string                   `json:"error,omitempty"`
	Assertions    []provenAssertion        `json:"assertions"`
	Packets       map[string]*packetResult `json:"packets"`
	Anomalies     []string                 `json:"anomalies,omitempty"`
	TestLog       string                   `json:"test_log,omitempty"`
	FixtureDigest string                   `json:"fixture_digest,omitempty"`
	RuntimeDigest string                   `json:"runtime_digest,omitempty"`
	Verified      bool                     `json:"verified"`
}
type report struct {
	Schema          int             `json:"schema_version"`
	SourcePin       string          `json:"source_pin"`
	Started         string          `json:"started_at"`
	Inputs          inputs          `json:"inputs"`
	InputsAfter     inputs          `json:"inputs_after"`
	Health          string          `json:"machine_health"`
	HealthErrors    []string        `json:"machine_health_errors,omitempty"`
	Requirements    int             `json:"requirements"`
	Invariants      int             `json:"invariants"`
	Upstream        targetResult    `json:"upstream"`
	Go              targetResult    `json:"go"`
	Gaps            []string        `json:"coverage_gaps"`
	InventoryReview string          `json:"independent_inventory_review"`
	Rollback        string          `json:"rollback"`
	SourceExecution sourceExecution `json:"source_execution"`
	Complete        bool            `json:"replacement_verified"`
}
type sourceExecution struct {
	Status   string   `json:"status"`
	RawFiles int      `json:"raw_v8_files"`
	Percent  *float64 `json:"line_branch_percent"`
	Note     string   `json:"note"`
}

func main() {
	node := flag.String("node", "node", "Node executable (the pinned upstream runtime requires Node 22)")
	phase := flag.String("target", "both", "upstream or both; Go replacement verdict is gated on complete upstream proof")
	output := flag.String("out", "", "new report path (default: a disposable work directory)")
	parallel := flag.Int("parallel", 4, "packet packages tested at the same time (go test -p)")
	packetList := flag.String("packets", "", "diagnostic: run only these comma-separated packets; no verdict, health checks or gate")
	impl := flag.String("impl", "upstream", "packet diagnostics use upstream; the complete verifier gates Go assessment")
	digest := flag.String("digest", "", "print the review digests of this packet and exit")
	showGaps := flag.Bool("gaps", false, "print the coverage gaps of the declared plan (no servers) and exit")
	flag.Parse()
	if *phase != "upstream" && *phase != "both" {
		fmt.Fprintln(os.Stderr, "target must be upstream or both")
		os.Exit(1)
	}
	if *parallel < 1 {
		fmt.Fprintln(os.Stderr, "parallel must be at least 1")
		os.Exit(1)
	}
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	work, err := os.MkdirTemp("", "abs-verify-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg := config{root: root, work: work, node: *node, parallel: *parallel}
	switch {
	case *digest != "":
		exitOnError(printDigests(cfg, *digest))
		return
	case *showGaps && *packetList != "":
		exitOnError(printPacketGaps(ctx, cfg, strings.Split(*packetList, ",")))
		return
	case *showGaps:
		exitOnError(printGaps(ctx, cfg))
		return
	case *packetList != "":
		failed, err := diagnose(ctx, cfg, strings.Split(*packetList, ","), *impl)
		exitOnError(err)
		if failed {
			os.Exit(1)
		}
		return
	}
	if *output == "" {
		*output = filepath.Join(work, "report.json")
	}
	result := report{Schema: 2, SourcePin: sourcePin, Started: time.Now().UTC().Format(time.RFC3339), Health: "not_run", Upstream: targetResult{Status: "not_run", Assertions: []provenAssertion{}}, Go: targetResult{Status: "blocked", Assertions: []provenAssertion{}, Error: "upstream coverage is not certified"}, Rollback: "blocked"}
	err = verify(ctx, cfg, *phase, &result)
	if err != nil {
		if result.Upstream.Verified {
			result.Go.Status = "failed"
			result.Go.Error = err.Error()
		} else {
			result.Upstream.Status = "failed"
			result.Upstream.Error = err.Error()
		}
	}
	if writeErr := writeReport(*output, result); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	fmt.Printf("Report: %s\nMachine health: %s\nUpstream: %s (%d concrete assertions; %d coverage gaps)\nGo replacement: %s\n", *output, result.Health, result.Upstream.Status, len(result.Upstream.Assertions), len(result.Gaps), result.Go.Status)
	printFailures("upstream", result.Upstream)
	printFailures("go", result.Go)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *phase == "upstream" && result.Upstream.Verified {
		fmt.Println("All requirements of the defined v2.37.2 contract are verified against upstream.")
		return
	}
	if !result.Complete {
		fmt.Println("100% upstream/replacement coverage is NOT confirmed.")
		os.Exit(2)
	}
	fmt.Println("All requirements of the defined v2.37.2 replacement contract are verified.")
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// printFailures lists up to 20 failed scenarios of a target with the first lines of their output.
func printFailures(target string, result targetResult) {
	shown := 0
	for _, name := range sortedPackets(result.Packets) {
		for _, f := range result.Packets[name].Failures {
			if shown == 20 {
				fmt.Printf("... more failures are in the report\n")
				return
			}
			shown++
			fmt.Printf("FAILED %s %s/%s\n", target, name, f.Assertion)
			for _, line := range f.Output[:min(len(f.Output), 8)] {
				fmt.Printf("    %s\n", line)
			}
		}
	}
	for _, anomaly := range result.Anomalies {
		fmt.Printf("ANOMALY %s: %s\n", target, anomaly)
	}
}

func sortedPackets(packets map[string]*packetResult) []string {
	names := make([]string, 0, len(packets))
	for name := range packets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// loadContract reads the capability records and packets.json.
func loadContract(root string) ([]capability, map[string][]string, error) {
	records, err := loadCapabilities(root)
	if err != nil {
		return nil, nil, err
	}
	packets, err := loadPackets(root)
	if err != nil {
		return nil, nil, err
	}
	return records, packets, nil
}

// printDigests prints the digests a review receipt of the packet must carry.
func printDigests(cfg config, packet string) error {
	records, packets, err := loadContract(cfg.root)
	if err != nil {
		return err
	}
	ids, ok := packets[packet]
	if !ok {
		return fmt.Errorf("%q is not a packet of packets.json", packet)
	}
	scenarios, err := scenarioDigest(cfg.root, packet)
	if err != nil {
		return err
	}
	requirements, err := requirementsDigest(records, ids)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(map[string]string{"scenario_digest": scenarios, "requirements_digest": requirements}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

// printPacketGaps prints the scenario coverage gaps of the named packets only,
// so one owner can check a packet while others are unfinished. Review receipts
// are not checked here.
func printPacketGaps(ctx context.Context, cfg config, names []string) error {
	records, packets, err := loadContract(cfg.root)
	if err != nil {
		return err
	}
	var packages []string
	owned := make(map[string]bool)
	for _, name := range names {
		ids, ok := packets[name]
		if !ok {
			return fmt.Errorf("%q is not a packet of packets.json", name)
		}
		packages = append(packages, "./tests/replacement/"+name)
		for _, id := range ids {
			owned[id] = true
		}
	}
	plan, err := discoverPlan(ctx, cfg, packages...)
	if err != nil {
		return err
	}
	if err := validatePlan(records, packets, plan); err != nil {
		return err
	}
	var selected []capability
	for _, record := range records {
		if owned[record.ID] {
			selected = append(selected, record)
		}
	}
	gaps := coverageGaps(selected, plan)
	for _, gap := range gaps {
		fmt.Println(gap)
	}
	fmt.Printf("%d declared scenarios; %d scenario coverage gaps in %s\n", len(plan), len(gaps), strings.Join(names, ","))
	return nil
}

// printGaps discovers the plan without servers and prints every coverage gap.
func printGaps(ctx context.Context, cfg config) error {
	records, packets, err := loadContract(cfg.root)
	if err != nil {
		return err
	}
	plan, err := discoverPlan(ctx, cfg, allPackets)
	if err != nil {
		return err
	}
	if err := validatePlan(records, packets, plan); err != nil {
		return err
	}
	gaps := coverageGaps(records, plan)
	reviewed, accepted := reviewGaps(cfg.root, records, packets, plan)
	gaps = append(gaps, reviewed...)
	for _, gap := range gaps {
		fmt.Println(gap)
	}
	fmt.Printf("%d declared scenarios in %d packets; %d coverage gaps; %d of %d packet reviews accepted\n", len(plan), countPackets(plan), len(gaps), accepted, len(packets))
	return nil
}

func countPackets(plan []harness.Declaration) int {
	seen := make(map[string]bool)
	for _, d := range plan {
		seen[d.Packet] = true
	}
	return len(seen)
}

// diagnose runs only the named packets against one implementation and prints a
// per-packet summary. It reports no verdict, runs no health checks and applies
// no gate; failed is true when any selected scenario fails.
func diagnose(ctx context.Context, cfg config, names []string, impl string) (failed bool, err error) {
	if impl != "upstream" {
		return false, fmt.Errorf("packet diagnostics require upstream; run make verify for the gated Go assessment")
	}
	records, packets, err := loadContract(cfg.root)
	if err != nil {
		return false, err
	}
	var packages []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, ok := packets[name]; !ok {
			return false, fmt.Errorf("%q is not a packet of packets.json", name)
		}
		packages = append(packages, "./tests/replacement/"+name)
	}
	if err := checkNode(ctx, cfg.node); err != nil {
		return false, err
	}
	plan, err := discoverPlan(ctx, cfg, packages...)
	if err != nil {
		return false, err
	}
	if err := validatePlan(records, packets, plan); err != nil {
		return false, err
	}
	result, _, err := runTarget(ctx, cfg, impl, plan, packages...)
	if err != nil {
		return false, err
	}
	for _, name := range sortedPackets(result.Packets) {
		packet := result.Packets[name]
		fmt.Printf("%s: declared %d, passed %d, failed %d\n", name, packet.Declared, packet.Passed, packet.Failed)
	}
	for _, name := range sortedPackets(result.Packets) {
		for _, f := range result.Packets[name].Failures {
			fmt.Printf("\nFAILED %s/%s (%s %s %s %s)\n", name, f.Assertion, f.Requirement, f.Invariant, f.Case, f.State)
			for _, line := range f.Output {
				fmt.Printf("    %s\n", line)
			}
		}
	}
	for _, anomaly := range result.Anomalies {
		fmt.Printf("ANOMALY: %s\n", anomaly)
	}
	fmt.Printf("\nlogs: %s\n", result.TestLog)
	return result.Status != "assertions_passed", nil
}

func verify(ctx context.Context, cfg config, phase string, result *report) error {
	root, work, node := cfg.root, cfg.work, cfg.node
	if err := checkNode(ctx, node); err != nil {
		return err
	}
	// Reuse the existing source pin/build guard, not another source oracle.
	if err := runCommand(ctx, root, filepath.Join(work, "prepare.log"), nil, node, "-e", "require('./go-backend/tools/parity/lib/oracle.cjs').prepareOracle()"); err != nil {
		return err
	}
	before, err := inputDigests(root, node)
	if err != nil {
		return err
	}
	result.Inputs = before
	records, packets, err := loadContract(root)
	if err != nil {
		return err
	}
	result.Requirements = len(records)
	for _, c := range records {
		result.Invariants += len(c.Invariants)
	}
	plan, err := discoverPlan(ctx, cfg, allPackets)
	if err != nil {
		return err
	}
	if err := validatePlan(records, packets, plan); err != nil {
		return err
	}
	result.Gaps = coverageGaps(records, plan)
	reviewed, accepted := reviewGaps(root, records, packets, plan)
	result.Gaps = append(result.Gaps, reviewed...)
	result.InventoryReview = fmt.Sprintf("%d of %d packet reviews accepted", accepted, len(packets))
	if gap := inventoryReviewGap(root, before); gap != "" {
		result.Gaps = append(result.Gaps, gap)
		result.InventoryReview += "; " + gap
	} else {
		result.InventoryReview += "; whole-contract inventory review accepted"
	}
	fmt.Println("Checking the unified machine (Go regressions and Node self-tests)...")
	health := []struct {
		name, dir, command string
		args               []string
	}{
		{"mapping", root, node, []string{"go-backend/tools/replacement-contract-map.mjs", "--check"}},
		{"go-build", filepath.Join(root, "go-backend"), "go", []string{"build", "./..."}},
		{"go-tests", filepath.Join(root, "go-backend"), "go", []string{"test", "./...", "-count=1"}},
		{"go-vet", filepath.Join(root, "go-backend"), "go", []string{"vet", "./..."}},
		{"go-vet-replacement", filepath.Join(root, "go-backend"), "go", []string{"vet", "-tags=replacement", "./..."}},
		{"go-lint", filepath.Join(root, "go-backend"), "golangci-lint", []string{"run", "--config=../.golangci.yml", "--timeout=5m", "--build-tags=replacement", "./..."}},
		{"verifier-race", filepath.Join(root, "go-backend"), "env", []string{"CGO_ENABLED=1", "go", "test", "-race", "-count=1", "./tools/verify", "./tests/replacement/harness"}},
		{"node-tests", root, node, []string{"--test", "go-backend/tools/verify-coverage.test.mjs", "go-backend/tools/source-inventory.test.mjs", "go-backend/tools/replacement-contract-map.test.mjs", "go-backend/tools/test-library-oracle.cjs", "go-backend/tools/test-book-item-oracle.cjs", "go-backend/tools/parity/test-compare-reports.cjs", "go-backend/tools/parity/test-status.cjs", "go-backend/tools/parity/test-cover-patch-contract.cjs", "go-backend/tools/parity/test-differential-selftest.cjs", "go-backend/tools/parity/test-playback-lifecycle.cjs"}},
		{"upstream-unit-tests", root, node, []string{"node_modules/mocha/bin/mocha.js", "dist-server/test", "--recursive"}},
	}
	result.Health = "passed"
	format := exec.CommandContext(ctx, "gofmt", "-l", filepath.Join(root, "go-backend"))
	formatted, formatErr := format.Output()
	if formatErr != nil || len(formatted) != 0 {
		result.Health = "failed"
		result.HealthErrors = append(result.HealthErrors, fmt.Sprintf("Go formatting failed: %v %s", formatErr, formatted))
	}
	for _, check := range health {
		if err := runCommand(ctx, check.dir, filepath.Join(work, check.name+".log"), nil, check.command, check.args...); err != nil {
			result.Health = "failed"
			result.HealthErrors = append(result.HealthErrors, err.Error())
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if err := runCommand(ctx, filepath.Join(root, "go-backend"), filepath.Join(work, "linux-"+arch+".log"),
			[]string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + arch}, "go", "build", "-trimpath", "-o", filepath.Join(work, "linux-"+arch), "./cmd/audiobookshelf-go"); err != nil {
			result.Health = "failed"
			result.HealthErrors = append(result.HealthErrors, err.Error())
		}
	}
	fmt.Println("Running the shared assertions against disposable pinned upstream...")
	upstream, execution, err := runTarget(ctx, cfg, "upstream", plan, allPackets)
	result.Upstream = upstream
	result.SourceExecution = execution
	if err != nil {
		return err
	}
	after, err := inputDigests(root, node)
	if err != nil {
		return err
	}
	result.InputsAfter = after
	if before != after {
		return fmt.Errorf("source, runtime assets, canonical requirements, shared assertions or starting fixture changed during verification")
	}
	if result.Health != "passed" {
		result.Gaps = append(result.Gaps, "machine-health checks failed; see individual logs")
	}
	if result.Upstream.Status != "assertions_passed" {
		return nil // the target result carries every failed scenario
	}
	if len(result.Gaps) != 0 {
		result.Upstream.Status = "partial"
		return nil
	}
	result.Upstream.Status = "verified"
	result.Upstream.Verified = true
	if phase == "upstream" {
		result.Go.Status = "not_requested"
		return nil
	}
	fmt.Println("Upstream coverage certified; running identical assertions against Go...")
	goResult, _, err := runTarget(ctx, cfg, "go", plan, allPackets)
	result.Go = goResult
	if err != nil {
		return err
	}
	if result.Go.Status != "assertions_passed" {
		return nil
	}
	if result.Upstream.FixtureDigest != result.Go.FixtureDigest {
		return fmt.Errorf("backends did not start with identical fixture contents")
	}
	result.Rollback = "running"
	rollbackEnv, err := harness.RuntimeEnvironment(root, filepath.Join(work, "rollback-environment"))
	if err != nil {
		result.Rollback = "failed"
		return err
	}
	if err := harness.RunIsolated(ctx, root, filepath.Join(work, "rollback.log"), rollbackEnv, node,
		"go-backend/tools/parity/replacement-trial.cjs", "--rollback-only", "--go-bin", filepath.Join(work, "go/audiobookshelf-go"), "--work", filepath.Join(work, "rollback")); err != nil {
		result.Rollback = "failed"
		return fmt.Errorf("rollback (log %s): %w", filepath.Join(work, "rollback.log"), err)
	}
	result.Rollback = "passed"
	after, err = inputDigests(root, node)
	if err != nil {
		return err
	}
	result.InputsAfter = after
	if before != after {
		return fmt.Errorf("verification inputs changed between upstream and Go")
	}
	// Rollback and all other state requirements must be bound in the shared plan
	// and reviewed in the packet receipts before this branch is reachable.
	result.Go.Status = "verified"
	result.Go.Verified = true
	result.Complete = true
	return nil
}

func writeReport(path string, value report) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".verify-report-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // atomic rename removes it on success
	_, writeErr := tmp.Write(append(raw, '\n'))
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
