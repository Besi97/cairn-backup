package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func base() Evidence {
	n := Step{Status: NotRun}
	return Evidence{Outcome: n, Selection: n, IdentityBefore: n, Extraction: n, IdentityAfter: n, ChecksState: n, Cleanup: n, Sample: Sample{Status: "not_run"}}
}
func TestRender(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	end := start.Add(time.Second)
	e := base()
	e.Outcome = Step{Status: Incomplete, Reason: "sample_failed"}
	e.Archive = &Archive{Name: "a\x1b\xff\n", ArchiveID: strings.Repeat("a", 64)}
	e.Scope.Paths = []string{"x\r\ny"}
	e.Selection = Step{Status: Passed}
	e.IdentityBefore = Step{Status: Passed}
	e.Extraction = Step{Status: Passed}
	e.IdentityAfter = Step{Status: Passed}
	e.ChecksState = Step{Status: Failed, Reason: "checks_failed"}
	e.Checks = []Check{{Kind: "file_bytes", Path: "secret\x1b", Status: "mismatch", Reason: "different_value"}, {Kind: "file_count", Path: ".", Status: "not_evaluated", Reason: "canceled"}}
	e.Sample = Sample{Status: "incomplete", Reason: "omitted", Method: "sha256-relative-path-lowest", EligibleComplete: true, Eligible: 5, Selected: 3, Unselected: 2, Compared: 2, Different: 1, Omitted: 1}
	e.Cleanup = Step{Status: Failed, Reason: "cleanup_failed"}
	e.Timing = Timing{&start, &end}
	for _, render := range []func(io.Writer, Evidence) error{WriteText, WriteJSON} {
		var b bytes.Buffer
		if err := render(&b, e); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b.Bytes(), []byte{0x1b}) || bytes.Contains(b.Bytes(), []byte{0xff}) || bytes.Contains(b.Bytes(), []byte{'\r'}) {
			t.Fatalf("unsafe output %q", b.String())
		}
		for _, want := range []string{"different_value", "omitted", "cleanup_failed", "sha256-relative-path-lowest"} {
			if !strings.Contains(b.String(), want) {
				t.Errorf("missing %s in %s", want, b.String())
			}
		}
	}
	var b bytes.Buffer
	if err := WriteJSON(&b, e); err != nil {
		t.Fatal(err)
	}
	var d document
	if err := json.Unmarshal(b.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Version != 1 || d.Evidence.Archive.Name != `"a\x1b\xff\n"` || d.Evidence.Sample.Omitted != 1 {
		t.Fatalf("bad document: %+v", d)
	}
	if e.Archive.Name != "a\x1b\xff\n" || e.Scope.Paths[0] != "x\r\ny" {
		t.Fatal("mutated input")
	}
}

func TestHistoryDetailsBoundedAndSafe(t *testing.T) {
	e := base()
	e.Archive = &Archive{Name: "a\x1b\n" + strings.Repeat("x", 300), ArchiveID: strings.Repeat("a", 64), RepositoryID: strings.Repeat("b", 64)}
	for i := 0; i < 40; i++ {
		e.Scope.Paths = append(e.Scope.Paths, "p\xff\n")
		e.Checks = append(e.Checks, Check{Kind: "file_bytes", Path: "c\x1b", Status: "mismatch", Reason: "different_value"})
	}
	h, err := ForHistory(e)
	if err != nil || !h.Valid() || !h.Details.Truncated || h.Details.PathsOmitted != 8 || h.Details.ChecksOmitted != 8 {
		t.Fatalf("%+v %v", h, err)
	}
	raw, err := json.Marshal(h)
	if err != nil || bytes.Contains(raw, []byte(strings.Repeat("b", 64))) || strings.Contains(HistoryText(h), "\x1b") {
		t.Fatalf("unsafe projection %s %v", raw, err)
	}
	var copy HistoryEvidence
	if json.Unmarshal(raw, &copy) != nil || !copy.Valid() || copy.Details.Paths[0] != `"p\xff\n"` {
		t.Fatalf("round trip %+v", copy)
	}
	copy.Details.Paths[0] = "raw\x1b"
	if copy.Valid() {
		t.Fatal("accepted unsafe display literal")
	}
	copy = h
	copy.Details = &HistoryDetails{PathsOmitted: -1}
	if copy.Valid() {
		t.Fatal("accepted negative omissions")
	}
}
func TestOutcomes(t *testing.T) {
	for _, status := range []Status{NotRun, Passed, Failed, Interrupted, Incomplete} {
		e := base()
		e.Outcome.Status = status
		var b bytes.Buffer
		if err := WriteText(&b, e); err != nil || !strings.Contains(b.String(), string(status)) {
			t.Fatalf("%s: %v %s", status, err, b.String())
		}
	}
	for _, status := range []string{"not_run", "passed", "different", "incomplete", "failed"} {
		e := base()
		e.Sample.Status = status
		if err := WriteJSON(io.Discard, e); err != nil {
			t.Fatal(status, err)
		}
	}
}
func TestInvalid(t *testing.T) {
	cases := []func(*Evidence){func(e *Evidence) { e.Selection.Status = "\x1b" }, func(e *Evidence) { e.Outcome.Reason = "secret" }, func(e *Evidence) { e.Archive = &Archive{ArchiveID: "bad"} }, func(e *Evidence) { e.Scope.Paths = []string{strings.Repeat("x", maxText+1)} }, func(e *Evidence) { e.Checks = make([]Check, maxItems+1) }, func(e *Evidence) { e.Checks = []Check{{Kind: "file_bytes", Status: "passed", Reason: "secret"}} }, func(e *Evidence) { e.Sample.Selected = 1; e.Sample.Compared = 2 }, func(e *Evidence) { a := time.Now(); b := a.Add(-time.Second); e.Timing = Timing{&a, &b} }}
	for i, change := range cases {
		e := base()
		change(&e)
		var b bytes.Buffer
		if err := WriteJSON(&b, e); err == nil || b.Len() != 0 {
			t.Errorf("case %d: %v %q", i, err, b.String())
		}
	}
}

type broken struct{}

func (broken) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func TestWriterError(t *testing.T) {
	for _, render := range []func(io.Writer, Evidence) error{WriteText, WriteJSON} {
		if render(broken{}, base()) == nil {
			t.Fatal("missing write error")
		}
	}
}
