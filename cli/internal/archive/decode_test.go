package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

const archiveJSON = `{"name":"before\nupgrade","id":"abc","start":"2026-11-15T10:00:00.000000"}`
const fileJSON = `{"path":"dir/file\nname","type":"-","mode":"-rw-------","size":42,"mtime":"2026-11-15T10:00:00.000000","uid":1000,"gid":100,"linktarget":"","healthy":true}`

func TestDecodeArchives(t *testing.T) {
	input := `{"repository":{"id":"repo"},"archives":[` + archiveJSON + `,` + archiveJSON + `],"encryption":{"mode":"none"}}`
	count := 0
	err := decodeArchives(strings.NewReader(input), func(entry Archive) error {
		count++
		if entry.Name != "before\nupgrade" || entry.ID != "abc" || entry.Start != "2026-11-15T10:00:00.000000" {
			t.Fatalf("archive = %+v", entry)
		}
		return nil
	})
	if err != nil || count != 2 {
		t.Fatalf("count = %d, error = %v", count, err)
	}
	if err := decodeArchives(strings.NewReader(`{"archives":[]}`), func(Archive) error {
		t.Fatal("unexpected archive")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeFiles(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		count := 0
		err := decodeFiles(strings.NewReader(fileJSON+"\n"+fileJSON+suffix), func(entry File) error {
			count++
			if entry.Path != "dir/file\nname" || entry.Size != 42 || !entry.Healthy || entry.UID != 1000 || entry.GID != 100 {
				t.Fatalf("file = %+v", entry)
			}
			return nil
		})
		if err != nil || count != 2 {
			t.Fatalf("count = %d, error = %v", count, err)
		}
	}
	for _, input := range []string{"", strings.Replace(fileJSON, `"healthy":true`, `"healthy":false`, 1)} {
		if err := decodeFiles(strings.NewReader(input), func(entry File) error {
			if entry.Healthy {
				t.Fatal("unhealthy file reported healthy")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRejectMalformedListings(t *testing.T) {
	for _, input := range []string{
		``, `[]`, `null`, `{}`, `{"archives":null}`, `{"archives":{}}`, `{"archives":[null]}`,
		`{"archives":[{}]}`, `{"archives":[{"name":true}]}`, `{"archives":[],"archives":[]}`,
		`{"archives":[` + archiveJSON, `{"archives":[]} true`, `{"archives":[],"other":[}`,
	} {
		t.Run("archive "+input, func(t *testing.T) {
			if err := decodeArchives(strings.NewReader(input), func(Archive) error { return nil }); err == nil {
				t.Fatal("accepted malformed listing")
			}
		})
	}
	for _, input := range []string{
		"\n", `[]`, `null`, `{}`, fileJSON + ` true`, fileJSON[:len(fileJSON)-1],
		strings.Replace(fileJSON, `"size":42`, `"size":null`, 1),
		strings.Replace(fileJSON, `"healthy":true`, `"healthy":null`, 1),
		strings.Replace(fileJSON, `"uid":1000`, `"uid":"name"`, 1),
	} {
		t.Run("file "+input, func(t *testing.T) {
			if err := decodeFiles(strings.NewReader(input), func(File) error { return nil }); err == nil {
				t.Fatal("accepted malformed file record")
			}
		})
	}
}

func TestRecordLimits(t *testing.T) {
	large := strings.Repeat("x", MaxRecordBytes)
	for _, input := range []string{
		`{"archives":[{"name":"` + large + `"}]}`,
		`{"metadata":"` + large + `","archives":[]}`,
		`{"` + large + `":[],"archives":[]}`,
	} {
		if err := decodeArchives(strings.NewReader(input), func(Archive) error { return nil }); !errors.Is(err, errRecordTooLarge) {
			t.Fatalf("oversized repository value error = %v", err)
		}
	}
	if err := decodeFiles(strings.NewReader(`{"path":"`+large+`"}`), func(File) error { return nil }); !errors.Is(err, errRecordTooLarge) {
		t.Fatalf("oversized file error = %v", err)
	}
	// A record near the cap must work even with read-ahead from the preceding
	// record. Unknown fields still count toward the bound.
	entry := strings.TrimSuffix(archiveJSON, "}") + `,"extra":"` + strings.Repeat("x", MaxRecordBytes-1024) + `"}`
	if err := decodeArchives(strings.NewReader(`{"archives":[`+archiveJSON+`,`+entry+`]}`), func(Archive) error { return nil }); err != nil {
		t.Fatalf("bounded record: %v", err)
	}
}

func TestStreamingListings(t *testing.T) {
	for _, files := range []bool{false, true} {
		t.Run(fmt.Sprint(files), func(t *testing.T) {
			reader, writer := io.Pipe()
			done := make(chan error, 1)
			const count = 100000
			go func() {
				defer writer.Close()
				if !files {
					_, _ = io.WriteString(writer, `{"archives":[`)
				}
				for i := range count {
					text := fileJSON + "\n"
					if !files {
						text = archiveJSON
						if i != count-1 {
							text += ","
						}
					}
					if _, err := io.WriteString(writer, text); err != nil {
						return
					}
				}
				if !files {
					_, _ = io.WriteString(writer, `]}`)
				}
			}()
			got := 0
			go func() {
				if files {
					done <- decodeFiles(reader, func(File) error { got++; return nil })
				} else {
					done <- decodeArchives(reader, func(Archive) error { got++; return nil })
				}
			}()
			if err := <-done; err != nil || got != count {
				t.Fatalf("records = %d, error = %v", got, err)
			}
		})
	}
}

func TestCallbackErrors(t *testing.T) {
	want := errors.New("consumer stopped")
	if err := decodeArchives(strings.NewReader(`{"archives":[`+archiveJSON+`]}`), func(Archive) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if err := decodeFiles(strings.NewReader(fileJSON), func(File) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func FuzzDecoders(f *testing.F) {
	for _, input := range []string{`{"archives":[` + archiveJSON + `]}`, fileJSON, `{"archives":[]}`} {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = decodeArchives(bytes.NewReader(data), func(Archive) error { return nil })
		_ = decodeFiles(bytes.NewReader(data), func(File) error { return nil })
	})
}

func TestDisplayTextJSON(t *testing.T) {
	var entry File
	if err := json.Unmarshal([]byte(fileJSON), &entry); err != nil || entry.Path != "dir/file\nname" {
		t.Fatalf("file = %+v, error = %v", entry, err)
	}
}
