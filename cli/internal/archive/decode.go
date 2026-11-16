package archive

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxRecordBytes bounds each JSON record, repository metadata value, and token,
// including intervening whitespace. Total listing length is not limited.
const MaxRecordBytes = 1 << 20

var errRecordTooLarge = errors.New("Borg listing record exceeds 1 MiB limit")

type budgetReader struct {
	reader    io.Reader
	remaining int64
	read      int64
}

func (r *budgetReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errRecordTooLarge
	}
	n, err := r.reader.Read(p[:min(int64(len(p)), r.remaining)])
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, err
}

type boundedDecoder struct {
	*json.Decoder
	reader *budgetReader
}

func newBoundedDecoder(r io.Reader) *boundedDecoder {
	reader := &budgetReader{reader: r}
	return &boundedDecoder{Decoder: json.NewDecoder(reader), reader: reader}
}

func (d *boundedDecoder) reset() {
	// Account for Decoder read-ahead so a single value cannot exceed the cap
	// by arriving partly in the previous read.
	d.reader.remaining = MaxRecordBytes - (d.reader.read - d.InputOffset())
}

func (d *boundedDecoder) token() (json.Token, error) {
	d.reset()
	return d.Token()
}

func (d *boundedDecoder) decode(value any) error {
	d.reset()
	return d.Decode(value)
}

func (d *boundedDecoder) more() bool {
	d.reset()
	return d.More()
}

func (d *boundedDecoder) expect(want json.Delim) error {
	token, err := d.token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("expected JSON %q in Borg listing", want)
	}
	return nil
}

func decodeArchives(r io.Reader, emit func(Archive) error) error {
	d := newBoundedDecoder(r)
	if err := d.expect('{'); err != nil {
		return err
	}
	found := false
	for d.more() {
		token, err := d.token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("expected repository JSON key")
		}
		if key != "archives" {
			var ignored json.RawMessage
			if err := d.decode(&ignored); err != nil {
				return err
			}
			continue
		}
		if found {
			return errors.New("duplicate archives field in Borg listing")
		}
		found = true
		if err := d.expect('['); err != nil {
			return err
		}
		for d.more() {
			var entry Archive
			if err := d.decode(&entry); err != nil {
				return err
			}
			if entry.Name == "" || entry.ID == "" || entry.Start == "" {
				return errors.New("Borg archive record is missing name, id, or start")
			}
			if err := emit(entry); err != nil {
				return err
			}
		}
		if err := d.expect(']'); err != nil {
			return err
		}
	}
	if err := d.expect('}'); err != nil {
		return err
	}
	if !found {
		return errors.New("Borg listing is missing archives")
	}
	_, err := d.token()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON in Borg listing")
}

func decodeFiles(r io.Reader, emit func(File) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), MaxRecordBytes+1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > MaxRecordBytes {
			return errRecordTooLarge
		}
		var entry struct {
			File
			Size    *int64 `json:"size"`
			UID     *int64 `json:"uid"`
			GID     *int64 `json:"gid"`
			Healthy *bool  `json:"healthy"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			return errors.New("invalid JSON file record in Borg listing")
		}
		if entry.Path == "" || len(entry.Type) != 1 || len(entry.Mode) != 10 ||
			entry.Mtime == "" || entry.Size == nil || entry.UID == nil || entry.GID == nil || entry.Healthy == nil {
			return errors.New("Borg file record is missing required metadata")
		}
		entry.File.Size, entry.File.UID, entry.File.GID, entry.File.Healthy = *entry.Size, *entry.UID, *entry.GID, *entry.Healthy
		if err := emit(entry.File); err != nil {
			return err
		}
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return errRecordTooLarge
	}
	return scanner.Err()
}
