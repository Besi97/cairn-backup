package borg

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestStructuredLogOutput(t *testing.T) {
	const input = `{"type":"log_message","levelname":"WARNING","message":"warning first\nwarning second"}
{"type":"progress_message","message":"loading"}
{"type":"progress_percent","message":"checking 25%"}
{"type":"archive_progress","original_size":1234,"compressed_size":1000,"deduplicated_size":500,"nfiles":2,"path":"résumé"}
{"type":"file_status","status":"A","path":"preview-file"}
{"type":"progress_percent","finished":true}
{"type":"new_record","message":"future human diagnostic"}
{"type":"new_record","value":42}
{"type":"progress_percent","value":"not a valid progress record"}
{"type":"log_message","message":"cursor \u001b[2J bell \u0007"}
not json` + "\r\n" + `trailing diagnostic`
	var output bytes.Buffer
	var progress []Progress
	w := &logOutput{writer: &output, progress: func(p Progress) error {
		progress = append(progress, p)
		return nil
	}}
	for _, b := range []byte(input) {
		if n, err := w.Write([]byte{b}); n != 1 || err != nil {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	want := "warning first\nwarning second\nA preview-file\nfuture human diagnostic\n" +
		"{\"type\":\"new_record\",\"value\":42}\n" +
		"{\"type\":\"progress_percent\",\"value\":\"not a valid progress record\"}\n" +
		"cursor \\x1b[2J bell \\a\nnot json\ntrailing diagnostic\n"
	if output.String() != want {
		t.Fatalf("diagnostics:\n%q\nwant:\n%q", output.String(), want)
	}
	if len(progress) != 4 || progress[0].Message != "loading" || progress[1].Message != "checking 25%" ||
		progress[2].Message != "1.23 kB O 1.00 kB C 500 B D 2 N résumé" || !progress[3].Finished {
		t.Fatalf("progress: %+v", progress)
	}
}

func TestStdoutDiagnosticsAreNotParsedAsRecords(t *testing.T) {
	var output bytes.Buffer
	w := &logOutput{writer: &output, raw: true}
	const data = "{\"type\":\"progress_message\",\"message\":\"literal output\"}\n\nlast"
	if _, err := io.WriteString(w, data); err != nil {
		t.Fatal(err)
	}
	if err := w.flush(); err != nil || output.String() != data+"\n" {
		t.Fatalf("output=%q error=%v", output.String(), err)
	}
}

func TestStructuredOutputErrors(t *testing.T) {
	want := errors.New("destination failed")
	for _, progress := range []bool{false, true} {
		w := &logOutput{writer: failingWriter{want}, progress: func(Progress) error { return want }}
		input := `{"type":"log_message","message":"warning"}` + "\n"
		if progress {
			input = `{"type":"progress_message","message":"checking"}` + "\n"
		}
		if _, err := io.WriteString(w, input); !errors.Is(err, want) {
			t.Fatalf("error=%v", err)
		}
		if !errors.Is(w.flush(), want) {
			t.Fatal("flush lost write failure")
		}
	}
}

func TestOversizedDiagnosticIsNotDropped(t *testing.T) {
	var output bytes.Buffer
	input := strings.Repeat("x", maxLogRecord+10)
	w := &logOutput{writer: &output, progress: func(Progress) error {
		t.Fatal("malformed output became progress")
		return nil
	}}
	if _, err := io.WriteString(w, input); err != nil {
		t.Fatal(err)
	}
	if err := w.flush(); err != nil || strings.ReplaceAll(output.String(), "\n", "") != input {
		t.Fatalf("output size=%d error=%v", output.Len(), err)
	}
}

func TestRunnerStructuredProgressIsOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		req := request()
		req.Env["FAKE_LOG_RECORDS"] = "{\"type\":\"progress_percent\",\"message\":\"checking 50%\"}\n" +
			"{\"type\":\"log_message\",\"message\":\"diagnostic survives\"}\n"
		var output bytes.Buffer
		req.Diagnostics = &output
		var progress []Progress
		if enabled {
			req.Progress = func(p Progress) error { progress = append(progress, p); return nil }
		}
		result, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req)
		if err != nil || result.Outcome != Success {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		if enabled {
			if len(progress) != 1 || progress[0].Message != "checking 50%" ||
				strings.Contains(output.String(), `"type"`) || strings.Contains(output.String(), "checking 50%") {
				t.Fatalf("progress=%+v diagnostics=%q", progress, output.String())
			}
		} else if len(progress) != 0 || !strings.Contains(output.String(), `"type"`) {
			t.Fatalf("non-human command output changed: %q", output.String())
		}
		if !strings.Contains(output.String(), "diagnostic survives") {
			t.Fatal("diagnostic dropped")
		}
	}
}

func TestSeparatedResultStdoutIsNeverDecodedOrFormatted(t *testing.T) {
	req := request()
	const data = "\r\x1b[2J{\"type\":\"progress_percent\",\"message\":\"result data\"}"
	req.Env["FAKE_STDOUT"] = data
	req.Env["FAKE_LOG_RECORDS"] = "{\"type\":\"progress_percent\",\"message\":\"real progress\"}\n"
	var results, diagnostics bytes.Buffer
	req.Stdout, req.Diagnostics = &results, &diagnostics
	calls := 0
	req.Progress = func(p Progress) error {
		if p.Message != "real progress" {
			t.Errorf("result data became progress: %+v", p)
		}
		calls++
		return nil
	}
	result, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req)
	if err != nil || result.Outcome != Success || calls != 1 {
		t.Fatalf("result=%+v error=%v progress calls=%d", result, err, calls)
	}
	if results.String() != "stdout diagnostic\n"+data || diagnostics.String() != "stderr diagnostic\n" {
		t.Fatalf("stdout=%q diagnostics=%q", results.String(), diagnostics.String())
	}
}
