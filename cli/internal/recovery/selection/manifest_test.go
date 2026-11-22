package selection

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBorgName(t *testing.T) {
	for _, tt := range []struct{ wire, want string }{
		{`"archive-[*]?{now}:\n\u00e9"`, "archive-[*]?{now}:\né"},
		{`"raw-\udc80\udcff"`, "raw-\x80\xff"},
		{`"not-raw-\ufffd"`, "not-raw-\uFFFD"},
		{`"\ud800\udc80"`, "\U00010080"},
		{`"\\udcff\""`, "\\udcff\""},
		{`"é"`, "é"},
		{`""`, ""},
		{`"\u0000"`, ""},
		{`"\ud800"`, ""},
		{`"\udc7f"`, ""},
		{`"a\/b"`, ""},
		{`null`, ""},
		{"\"\xff\"", ""},
	} {
		var name borgName
		err := json.Unmarshal([]byte(tt.wire), &name)
		if string(name) != tt.want || (err != nil) != (tt.want == "") {
			t.Errorf("%q: got %q, %v; want %q", tt.wire, name, err, tt.want)
		}
	}
}

func TestReadManifest(t *testing.T) {
	id := strings.Repeat("1234", 16)
	repo := `{"id":"` + id + `"}`
	entry := `{"name":"raw-?","archive":"raw-?","barchive":"raw-\udcff","id":"` + id + `"}`
	listing := `{"archives":[` + entry + `],"repository":` + repo + `}`
	if got, err := readManifest(strings.NewReader(listing), Selector{Exact: "raw-\xff"}); err != nil || got != (Candidate{
		Name: "raw-\xff", ArchiveID: id, RepositoryID: id,
	}) {
		t.Fatalf("exact candidate = %+v, %v", got, err)
	}
	if got, err := readManifest(strings.NewReader(listing), Selector{Exact: "raw-?"}); err != nil || got.Name != "" {
		t.Fatalf("display name selected: %+v, %v", got, err)
	}
	for _, input := range []string{
		`{"archives":[` + entry + `]}`,
		`{"archives":[` + entry + `,` + entry + `],"repository":` + repo + `}`,
		`{"archives":[{"name":"raw-?"}],"repository":` + repo + `}`,
		`{"archives":[],"repository":{"id":"short"}}`,
		`{"archives":[],"archives":[],"repository":` + repo + `}`,
		listing + "{}",
		listing + strings.Repeat(" ", maxRecordBytes) + "true",
		strings.TrimSuffix(listing, "}"),
	} {
		if _, err := readManifest(strings.NewReader(input), Selector{Exact: "raw-\xff"}); err == nil {
			t.Errorf("accepted malformed or oversized listing (length %d)", len(input))
		}
	}
}

func TestReadManifestStreamsPastRecordLimit(t *testing.T) {
	id := strings.Repeat("a", 64)
	record := `{"barchive":"other","id":"` + id + `"}`
	input := `{"repository":{"id":"` + id + `"},"archives":[` +
		strings.Repeat(record+",", maxRecordBytes/len(record)+1) +
		`{"barchive":"chosen","id":"` + id + `"}]}`
	got, err := readManifest(strings.NewReader(input), Selector{Exact: "chosen"})
	if err != nil || got.Name != "chosen" {
		t.Fatalf("long listing: %+v, %v", got, err)
	}
}
