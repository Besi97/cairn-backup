package mount

import (
	"strings"
	"testing"
)

func TestVerifyMountMetadata(t *testing.T) {
	const valid = `20 1 0:40 / /mnt/archive\040space rw,nosuid,nodev - fuse borgfs ro,user_id=1000,group_id=1000`
	for _, tt := range []struct {
		name, data, target string
		ok                 bool
	}{
		{"native Borg", valid, "/mnt/archive space", true},
		{"Borg subtype", strings.Replace(valid, "- fuse ", "- fuse.borgfs ", 1), "/mnt/archive space", true},
		{"missing", "", "/mnt/archive space", false},
		{"descendant", valid, "/mnt/archive space/file", false},
		{"prefix", valid, "/mnt/archive", false},
		{"stacked", valid + "\n" + valid, "/mnt/archive space", false},
		{"other UID", strings.Replace(valid, "user_id=1000", "user_id=1001", 1), "/mnt/archive space", false},
		{"missing UID", strings.Replace(valid, "user_id=1000,", "", 1), "/mnt/archive space", false},
		{"duplicate UID", valid + ",user_id=1000", "/mnt/archive space", false},
		{"empty UID", strings.Replace(valid, "user_id=1000", "user_id=", 1), "/mnt/archive space", false},
		{"misleading UID", strings.Replace(valid, "user_id=1000", "user_id=01000", 1), "/mnt/archive space", false},
		{"other FUSE", strings.Replace(valid, " borgfs ", " sshfs ", 1), "/mnt/archive space", false},
		{"other type", strings.Replace(valid, "- fuse ", "- ext4 ", 1), "/mnt/archive space", false},
		{"other subtype", strings.Replace(valid, "- fuse ", "- fuse.sshfs ", 1), "/mnt/archive space", false},
		{"bind subtree", strings.Replace(valid, " / /mnt", " /subtree /mnt", 1), "/mnt/archive space", false},
		{"malformed", "20 1 0:40 / /mnt rw", "/mnt", false},
		{"bad escape", strings.Replace(valid, `\040`, `\041`, 1), "/mnt/archive space", false},
		{"extra fields", valid + " extra", "/mnt/archive space", false},
		{"relative target", strings.Replace(valid, "/mnt/", "mnt/", 1), "mnt/archive space", false},
		{"unclean target", strings.Replace(valid, "/mnt/", "/mnt/../mnt/", 1), "/mnt/archive space", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := parseMounts(strings.NewReader(tt.data))
			if err == nil {
				err = verify(entries, tt.target, 1000)
			}
			if (err == nil) != tt.ok {
				t.Fatalf("error = %v, want success %t", err, tt.ok)
			}
		})
	}
}

func TestMountMetadataEscapes(t *testing.T) {
	target := "/mnt/a b\tc\nd\\e"
	line := `20 1 0:40 / /mnt/a\040b\011c\012d\134e rw shared:2 - fuse borgfs ro,user_id=1000`
	entries, err := parseMounts(strings.NewReader(line))
	if err != nil || verify(entries, target, 1000) != nil {
		t.Fatalf("entries = %#v, error = %v", entries, err)
	}
	for _, value := range []string{`\`, `\04`, `\000`, `\777`} {
		if _, err := unescape(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if _, err := parseMounts(strings.NewReader(strings.Repeat("x", 1024*1024+1))); err == nil {
		t.Fatal("accepted oversized metadata")
	}
}

func TestActualMountMetadataParses(t *testing.T) {
	entries, err := readMounts()
	if err != nil || len(entries) == 0 {
		t.Fatalf("entries=%d, error=%v", len(entries), err)
	}
}
