package selection

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// A limit per record (including whitespace), not per repository. Only the
// winning entry is retained, so large repositories don't fill Cairn's memory.
const maxRecordBytes = 1 << 20

type manifestDecoder struct {
	*json.Decoder
	reader *io.LimitedReader
	offset int64
}

func (d *manifestDecoder) resetBudget() {
	offset := d.InputOffset()
	// Decoder read-ahead is still charged against the next value.
	d.reader.N += offset - d.offset
	d.offset = offset
}

func (d *manifestDecoder) token() (json.Token, error) {
	d.resetBudget()
	return d.Token()
}

func (d *manifestDecoder) decode(value any) error {
	d.resetBudget()
	return d.Decode(value)
}

func (d *manifestDecoder) expect(want json.Delim) error {
	got, err := d.token()
	if err == nil && got != want {
		err = fmt.Errorf("expected %q in Borg manifest listing", want)
	}
	return err
}

func (d *manifestDecoder) more() bool {
	d.resetBudget()
	return d.More()
}

func readManifest(r io.Reader, selector Selector) (Candidate, error) {
	reader := &io.LimitedReader{R: r, N: maxRecordBytes}
	d := manifestDecoder{Decoder: json.NewDecoder(reader), reader: reader}
	var candidate Candidate
	if err := d.expect('{'); err != nil {
		return Candidate{}, err
	}
	var haveArchives bool
	for d.more() {
		key, err := d.token()
		if err != nil {
			return Candidate{}, err
		}
		switch key {
		case "archives":
			if haveArchives {
				return Candidate{}, errors.New("duplicate Borg archives field")
			}
			haveArchives = true
			candidate.Name, candidate.ArchiveID, err = d.readArchives(selector)
		case "repository":
			if candidate.RepositoryID != "" {
				return Candidate{}, errors.New("duplicate Borg repository field")
			}
			var repo struct {
				ID string `json:"id"`
			}
			if err = d.decode(&repo); err == nil {
				if !validID(repo.ID) {
					return Candidate{}, errors.New("invalid Borg repository ID")
				}
				candidate.RepositoryID = repo.ID
			}
		default:
			var ignored json.RawMessage
			err = d.decode(&ignored)
		}
		if err != nil {
			return Candidate{}, err
		}
	}
	if err := d.expect('}'); err != nil {
		return Candidate{}, err
	}
	if !haveArchives || candidate.RepositoryID == "" {
		return Candidate{}, errors.New("Borg listing is missing archives or repository identity")
	}
	_, err := d.token()
	if !errors.Is(err, io.EOF) || reader.N == 0 {
		return Candidate{}, errors.New("unexpected data after Borg manifest listing")
	}
	return candidate, nil
}

func (d *manifestDecoder) readArchives(selector Selector) (name, id string, err error) {
	if err := d.expect('['); err != nil {
		return "", "", err
	}
	for d.more() {
		var entry struct {
			BArchive borgName `json:"barchive"`
			ID       string   `json:"id"`
		}
		if err := d.decode(&entry); err != nil {
			return "", "", err
		}
		if entry.BArchive == "" || !validID(entry.ID) {
			return "", "", errors.New("Borg entry is missing a byte-exact name or valid archive ID")
		}
		if selector.matches(string(entry.BArchive)) {
			if selector.Exact != "" && name != "" {
				return "", "", errors.New("duplicate exact archive in Borg listing")
			}
			name, id = string(entry.BArchive), entry.ID
		}
	}
	return name, id, d.expect(']')
}

func validID(id string) bool {
	var buf [32]byte
	if len(id) != hex.EncodedLen(len(buf)) {
		return false
	}
	_, err := hex.Decode(buf[:], []byte(id))
	return err == nil
}

// Borg's non-JSON output replaces surrogates even in {barchive}. JSON barchive
// retains Python's surrogateescape bytes as lone \udc80 through \udcff escapes.
// Go's normal JSON string decoder replaces those with U+FFFD, so decode them
// separately while still treating a genuine UTF-16 pair as a Unicode scalar.
type borgName string

func (n *borgName) UnmarshalJSON(value []byte) error {
	invalid := errors.New("invalid byte-exact Borg archive name")
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' || !utf8.Valid(value) {
		return invalid
	}
	var decoded strings.Builder
	for i := 1; i < len(value)-1; {
		if value[i] != '\\' {
			decoded.WriteByte(value[i])
			i++
			continue
		}
		if value[i+1] != 'u' {
			// Reuse JSON's rules for ordinary two-byte escapes.
			var s string
			if err := json.Unmarshal([]byte{'"', '\\', value[i+1], '"'}, &s); err != nil {
				return invalid
			}
			decoded.WriteString(s)
			i += 2
			continue
		}
		unit, ok := unicodeEscape(value[i:])
		if !ok {
			return invalid
		}
		i += 6
		switch {
		case unit >= 0xd800 && unit <= 0xdbff:
			low, ok := unicodeEscape(value[i:])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return invalid
			}
			decoded.WriteRune(utf16.DecodeRune(unit, low))
			i += 6
		case unit >= 0xdc80 && unit <= 0xdcff:
			decoded.WriteByte(byte(unit - 0xdc00))
		case utf16.IsSurrogate(unit):
			return invalid
		default:
			decoded.WriteRune(unit)
		}
	}
	name := decoded.String()
	if name == "" || strings.ContainsAny(name, "/\x00") {
		return invalid
	}
	*n = borgName(name)
	return nil
}

func unicodeEscape(value []byte) (rune, bool) {
	if len(value) < 6 || value[0] != '\\' || value[1] != 'u' {
		return 0, false
	}
	unit, err := strconv.ParseUint(string(value[2:6]), 16, 16)
	return rune(unit), err == nil
}
