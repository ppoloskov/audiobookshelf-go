package oracle

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const maxDivergenceFileBytes = 16 << 20

// DivergenceRule describes an acknowledged behavioral divergence between
// TypeScript and Go servers that can filter known findings when Go matches
// expected behavior.
type DivergenceRule struct {
	ID              string `json:"id"`
	Scope           string `json:"scope"`
	Surface         string `json:"surface"`
	StepPattern     string `json:"step_pattern,omitempty"`
	PathPattern     string `json:"path_pattern,omitempty"`
	FieldPattern    string `json:"field_pattern,omitempty"`
	TSExpect        string `json:"ts_expect,omitempty"`
	GoExpect        string `json:"go_expect,omitempty"`
	IgnoreErrorBody bool   `json:"ignore_error_body,omitempty"`
	Reason          string `json:"reason"`
	ClientImpact    string `json:"client_impact"`
}

// DivergenceLedger holds the set of acknowledged divergence rules.
type DivergenceLedger struct {
	Rules []DivergenceRule `json:"rules"`
}

// LoadDivergences loads a JSON divergence ledger from disk after verifying the
// file size is within bounds.
func LoadDivergences(filePath string) (*DivergenceLedger, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat divergences %s: %w", filePath, err)
	}
	if info.Size() > maxDivergenceFileBytes {
		return nil, fmt.Errorf("divergences file %s exceeds %d bytes", filePath, maxDivergenceFileBytes)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read divergences %s: %w", filePath, err)
	}
	var ledger DivergenceLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, fmt.Errorf("decode divergences %s: %w", filePath, err)
	}
	return &ledger, nil
}

// DefaultDivergences loads divergences.json from the oracle package directory,
// returning an empty ledger if the file does not exist yet.
func DefaultDivergences() *DivergenceLedger {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return &DivergenceLedger{}
	}
	ledgerPath := filepath.Join(filepath.Dir(file), "divergences.json")
	ledger, err := LoadDivergences(ledgerPath)
	if err != nil {
		return &DivergenceLedger{}
	}
	return ledger
}

// Match returns the first DivergenceRule matching f, along with true if found.
// When rule.GoExpect is non-empty, the rule matches only if f.GoValue equals or
// contains rule.GoExpect so unexpected Go regressions are never hidden.
func (l *DivergenceLedger) Match(f Finding) (DivergenceRule, bool) {
	if l == nil {
		return DivergenceRule{}, false
	}
	for _, rule := range l.Rules {
		if !matchSurface(rule.Surface, f.Surface) {
			continue
		}
		if !matchPattern(rule.StepPattern, f.Step) {
			continue
		}
		if !matchPattern(rule.PathPattern, f.Path) {
			continue
		}
		if !matchPattern(rule.FieldPattern, f.Path) {
			continue
		}
		if rule.GoExpect != "" && f.GoValue != rule.GoExpect && !strings.Contains(f.GoValue, rule.GoExpect) {
			continue
		}
		return rule, true
	}
	return DivergenceRule{}, false
}

func (l *DivergenceLedger) shouldIgnoreErrorBody(step string) bool {
	if l == nil {
		return false
	}
	for _, rule := range l.Rules {
		if !rule.IgnoreErrorBody {
			continue
		}
		if !matchSurface(rule.Surface, "http") {
			continue
		}
		if matchPattern(rule.StepPattern, step) {
			return true
		}
	}
	return false
}

func matchSurface(ruleSurface, findingSurface string) bool {
	return ruleSurface == "" || ruleSurface == "*" || ruleSurface == findingSurface
}

func matchPattern(pattern, value string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		matched, err := path.Match(pattern, value)
		return err == nil && matched
	}
	return strings.Contains(value, pattern)
}
