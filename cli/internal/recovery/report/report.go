// Package report defines recovery evidence. Reports can contain sensitive
// archive names and paths; history retains a bounded display-only projection.
package report

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const Version = 1
const maxItems = 1000
const maxSampleFiles = 10000
const maxText = 4096

type Status string

const (
	NotRun      Status = "not_run"
	Passed      Status = "passed"
	Failed      Status = "failed"
	Interrupted Status = "interrupted"
	Incomplete  Status = "incomplete"
)

// Step records an outcome and a fixed reason code, never an error message.
type Step struct {
	Status Status `json:"status"`
	Reason string `json:"reason,omitempty"`
}
type Archive struct {
	Name         string `json:"name_display"`
	ArchiveID    string `json:"archive_id,omitempty"`
	RepositoryID string `json:"repository_id,omitempty"`
}
type Scope struct {
	WholeArchive bool     `json:"whole_archive"`
	Paths        []string `json:"paths_display"`
}
type Check struct {
	Kind   string `json:"kind"`
	Path   string `json:"path_display"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// EligibleComplete is false when selection failed partway through traversal.
type Sample struct {
	Status           string `json:"status"`
	Reason           string `json:"reason,omitempty"`
	Method           string `json:"method,omitempty"`
	EligibleComplete bool   `json:"eligible_complete"`
	Eligible         int64  `json:"eligible"`
	Selected         int    `json:"selected"`
	Unselected       int64  `json:"unselected"`
	Compared         int    `json:"compared"`
	Different        int    `json:"different"`
	Omitted          int    `json:"omitted"`
}
type Timing struct {
	Started  *time.Time `json:"started,omitempty"`
	Finished *time.Time `json:"finished,omitempty"`
}

// Evidence is an input value. Name and paths are byte strings, not JSON or
// terminal text. Renderers quote their bytes for display; never use displayed
// values as selectors. Do not put credentials, raw errors or diagnostics here.
type Evidence struct {
	Outcome        Step     `json:"outcome"`
	Archive        *Archive `json:"archive,omitempty"`
	Scope          Scope    `json:"scope"`
	Selection      Step     `json:"selection"`
	IdentityBefore Step     `json:"identity_before"`
	Extraction     Step     `json:"extraction"`
	IdentityAfter  Step     `json:"identity_after"`
	ChecksState    Step     `json:"checks_state"`
	Checks         []Check  `json:"checks"`
	Sample         Sample   `json:"sample"`
	Cleanup        Step     `json:"cleanup"`
	Timing         Timing   `json:"timing"`
}

type HistoryEvidence struct {
	Version        int             `json:"version"`
	Outcome        Step            `json:"outcome"`
	Selection      Step            `json:"selection"`
	IdentityBefore Step            `json:"identity_before"`
	Extraction     Step            `json:"extraction"`
	IdentityAfter  Step            `json:"identity_after"`
	ChecksState    Step            `json:"checks_state"`
	Sample         Sample          `json:"sample"`
	Cleanup        Step            `json:"cleanup"`
	Details        *HistoryDetails `json:"details,omitempty"`
	DetailsOmitted bool            `json:"details_omitted,omitempty"`
}

const historyItems = 32
const historyText = 256

type HistoryDetails struct {
	ArchiveName   string   `json:"archive_name_display,omitempty"`
	ArchiveID     string   `json:"archive_id,omitempty"`
	WholeArchive  bool     `json:"whole_archive"`
	Paths         []string `json:"paths_display,omitempty"`
	Checks        []Check  `json:"checks,omitempty"`
	PathsOmitted  int      `json:"paths_omitted,omitempty"`
	ChecksOmitted int      `json:"checks_omitted,omitempty"`
	Truncated     bool     `json:"truncated,omitempty"`
}

// ForHistory produces display-only values, never selectors or expected values.
func ForHistory(e Evidence) (HistoryEvidence, error) {
	if _, err := prepare(e); err != nil {
		return HistoryEvidence{}, err
	}
	h := HistoryEvidence{Version: Version, Outcome: e.Outcome, Selection: e.Selection, IdentityBefore: e.IdentityBefore, Extraction: e.Extraction,
		IdentityAfter: e.IdentityAfter, ChecksState: e.ChecksState, Sample: e.Sample, Cleanup: e.Cleanup}
	d := &HistoryDetails{WholeArchive: e.Scope.WholeArchive}
	text := func(s string) string {
		if len(s) > historyText {
			s = s[:historyText]
			d.Truncated = true
		}
		return display(s)
	}
	if e.Archive != nil {
		d.ArchiveName, d.ArchiveID = text(e.Archive.Name), e.Archive.ArchiveID
	}
	for _, p := range e.Scope.Paths[:min(len(e.Scope.Paths), historyItems)] {
		d.Paths = append(d.Paths, text(p))
	}
	d.PathsOmitted = len(e.Scope.Paths) - len(d.Paths)
	for _, c := range e.Checks[:min(len(e.Checks), historyItems)] {
		c.Path = text(c.Path)
		d.Checks = append(d.Checks, c)
	}
	d.ChecksOmitted = len(e.Checks) - len(d.Checks)
	h.Details = d
	if !h.Valid() {
		return HistoryEvidence{}, invalid
	}
	return h, nil
}

func (h HistoryEvidence) Valid() bool {
	if h.Version != Version {
		return false
	}
	_, err := prepare(Evidence{Outcome: h.Outcome, Selection: h.Selection,
		IdentityBefore: h.IdentityBefore, Extraction: h.Extraction,
		IdentityAfter: h.IdentityAfter, ChecksState: h.ChecksState,
		Sample: h.Sample, Cleanup: h.Cleanup})
	if err != nil || (h.Details != nil && h.DetailsOmitted) {
		return false
	}
	d := h.Details
	if d == nil {
		return true
	}
	safe := func(s string) bool {
		raw, err := strconv.Unquote(s)
		return err == nil && len(raw) <= historyText && display(raw) == s
	}
	if (d.ArchiveName != "" && !safe(d.ArchiveName)) || !validID(d.ArchiveID) ||
		len(d.Paths) > historyItems || len(d.Checks) > historyItems ||
		d.PathsOmitted < 0 || d.PathsOmitted > maxItems || d.ChecksOmitted < 0 || d.ChecksOmitted > maxItems ||
		len(d.Paths)+d.PathsOmitted > maxItems || len(d.Checks)+d.ChecksOmitted > maxItems {
		return false
	}
	for _, p := range d.Paths {
		if !safe(p) {
			return false
		}
	}
	for _, c := range d.Checks {
		if !safe(c.Path) || !oneOf(c.Kind, "path_type", "file_size", "file_bytes", "file_count", "file_sha256") ||
			!oneOf(c.Status, "passed", "mismatch", "evaluation_failed", "not_evaluated") ||
			!oneOf(c.Reason, "", "missing_path", "wrong_type", "different_value", "invalid_check", "root_unavailable", "parent_not_directory", "filesystem_failure", "canceled") {
			return false
		}
	}
	return true
}

// HistoryText renders previously validated display literals without decoding them.
func HistoryText(h HistoryEvidence) string {
	if !h.Valid() {
		return "invalid recovery evidence"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "outcome=%s reason=%s\n", h.Outcome.Status, h.Outcome.Reason)
	for _, x := range []struct {
		name string
		step Step
	}{
		{"selection", h.Selection}, {"identity before", h.IdentityBefore}, {"extraction", h.Extraction},
		{"identity after", h.IdentityAfter}, {"checks", h.ChecksState}, {"cleanup", h.Cleanup},
	} {
		fmt.Fprintf(&b, "%s=%s %s\n", x.name, x.step.Status, x.step.Reason)
	}
	fmt.Fprintf(&b, "sample=%s %s eligible=%d complete=%t selected=%d compared=%d different=%d omitted=%d\n",
		h.Sample.Status, h.Sample.Reason, h.Sample.Eligible, h.Sample.EligibleComplete, h.Sample.Selected, h.Sample.Compared, h.Sample.Different, h.Sample.Omitted)
	if h.DetailsOmitted {
		b.WriteString("details omitted (snapshot limit)\n")
	}
	if d := h.Details; d != nil {
		if d.ArchiveName != "" {
			fmt.Fprintf(&b, "archive=%s id=%s\n", d.ArchiveName, d.ArchiveID)
		}
		fmt.Fprintf(&b, "whole archive=%t\n", d.WholeArchive)
		for _, p := range d.Paths {
			fmt.Fprintf(&b, "path: %s\n", p)
		}
		for _, c := range d.Checks {
			fmt.Fprintf(&b, "check %s %s: %s %s\n", c.Kind, c.Path, c.Status, c.Reason)
		}
		fmt.Fprintf(&b, "paths omitted=%d checks omitted=%d text truncated=%t\n", d.PathsOmitted, d.ChecksOmitted, d.Truncated)
	}
	return b.String()
}

type document struct {
	Version  int      `json:"version"`
	Evidence Evidence `json:"evidence"`
}

var invalid = errors.New("invalid recovery evidence")

func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func validStep(s Step) bool {
	return oneOf(string(s.Status), "not_run", "passed", "failed", "interrupted", "incomplete") &&
		oneOf(s.Reason, "", "canceled", "selection_failed", "identity_changed", "identity_check_failed", "extraction_failed", "extraction_warning", "checks_failed", "sample_failed", "cleanup_failed", "shutdown_unconfirmed")
}
func display(s string) string { return strconv.QuoteToASCII(s) }
func validID(s string) bool {
	if s == "" {
		return true
	}
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// prepare validates bounds and makes an independent display-safe copy.
func prepare(e Evidence) (document, error) {
	if !validStep(e.Outcome) || !validStep(e.Selection) || !validStep(e.IdentityBefore) || !validStep(e.Extraction) || !validStep(e.IdentityAfter) || !validStep(e.ChecksState) || !validStep(e.Cleanup) || len(e.Scope.Paths) > maxItems || len(e.Checks) > maxItems {
		return document{}, invalid
	}
	if e.Archive != nil {
		a := *e.Archive
		if len(a.Name) > maxText || !validID(a.ArchiveID) || !validID(a.RepositoryID) {
			return document{}, invalid
		}
		a.Name = display(a.Name)
		e.Archive = &a
	}
	e.Scope.Paths = append([]string{}, e.Scope.Paths...)
	for i, p := range e.Scope.Paths {
		if len(p) > maxText {
			return document{}, invalid
		}
		e.Scope.Paths[i] = display(p)
	}
	e.Checks = append([]Check{}, e.Checks...)
	for i := range e.Checks {
		c := &e.Checks[i]
		if len(c.Path) > maxText || !oneOf(c.Kind, "path_type", "file_size", "file_bytes", "file_count", "file_sha256") || !oneOf(c.Status, "passed", "mismatch", "evaluation_failed", "not_evaluated") || !oneOf(c.Reason, "", "missing_path", "wrong_type", "different_value", "invalid_check", "root_unavailable", "parent_not_directory", "filesystem_failure", "canceled") {
			return document{}, invalid
		}
		c.Path = display(c.Path)
	}
	s := e.Sample
	if !oneOf(s.Status, "not_run", "passed", "different", "incomplete", "failed") || !oneOf(s.Method, "", "sha256-relative-path-lowest") || !oneOf(s.Reason, "", "empty", "omitted", "invalid_limit", "root_unavailable", "filesystem_failure", "canceled") || s.Eligible < 0 || s.Selected < 0 || s.Unselected < 0 || s.Compared < 0 || s.Different < 0 || s.Omitted < 0 || s.Selected > maxSampleFiles || s.Compared > s.Selected || s.Different > s.Compared || s.Omitted > s.Selected-s.Compared {
		return document{}, invalid
	}
	if e.Timing.Started != nil && e.Timing.Finished != nil && e.Timing.Finished.Before(*e.Timing.Started) {
		return document{}, invalid
	}
	return document{Version, e}, nil
}

// WriteJSON writes versioned evidence. Display strings are quoted byte literals.
func WriteJSON(w io.Writer, e Evidence) error {
	d, err := prepare(e)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// WriteText writes human-readable evidence without terminal control sequences.
func WriteText(w io.Writer, e Evidence) error {
	d, err := prepare(e)
	if err != nil {
		return err
	}
	e = d.Evidence
	var b strings.Builder
	fmt.Fprintf(&b, "Recovery evidence v%d\nOutcome: %s %s\n", Version, e.Outcome.Status, e.Outcome.Reason)
	if e.Archive == nil {
		b.WriteString("Archive: not selected\n")
	} else {
		fmt.Fprintf(&b, "Archive: %s\nArchive ID: %s\nRepository ID: %s\n", e.Archive.Name, e.Archive.ArchiveID, e.Archive.RepositoryID)
	}
	fmt.Fprintf(&b, "Scope: whole archive=%t\n", e.Scope.WholeArchive)
	for _, p := range e.Scope.Paths {
		fmt.Fprintf(&b, "  path: %s\n", p)
	}
	for _, x := range []struct {
		name string
		step Step
	}{{"Selection", e.Selection}, {"Identity before", e.IdentityBefore}, {"Extraction", e.Extraction}, {"Identity after", e.IdentityAfter}, {"Checks", e.ChecksState}, {"Cleanup", e.Cleanup}} {
		fmt.Fprintf(&b, "%s: %s %s\n", x.name, x.step.Status, x.step.Reason)
	}
	b.WriteString("Identity checks detect observed changes, not replacement and reversion or the identity seen by extraction.\n")
	if len(e.Checks) == 0 {
		b.WriteString("Checks: none recorded\n")
	}
	for _, c := range e.Checks {
		fmt.Fprintf(&b, "Check %s %s: %s %s\n", c.Kind, c.Path, c.Status, c.Reason)
	}
	s := e.Sample
	fmt.Fprintf(&b, "Sample: %s %s; method=%s; eligible=%d (complete=%t), selected=%d, unselected=%d, compared=%d, different=%d, omitted=%d\n", s.Status, s.Reason, s.Method, s.Eligible, s.EligibleComplete, s.Selected, s.Unselected, s.Compared, s.Different, s.Omitted)
	if e.Timing.Started != nil {
		fmt.Fprintf(&b, "Started: %s\n", e.Timing.Started.UTC().Format(time.RFC3339Nano))
	} else {
		b.WriteString("Started: not recorded\n")
	}
	if e.Timing.Finished != nil {
		fmt.Fprintf(&b, "Finished: %s\n", e.Timing.Finished.UTC().Format(time.RFC3339Nano))
	} else {
		b.WriteString("Finished: not recorded\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}
