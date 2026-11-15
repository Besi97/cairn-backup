package schedule

import (
	"strings"
	"testing"
	"time"
)

func instant(t *testing.T, text string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestNext(t *testing.T) {
	for _, tt := range []struct {
		name, expression, timezone, after, want string
	}{
		{"next minute", "* * * * *", "UTC", "2026-01-01T00:00:00Z", "2026-01-01T00:01:00Z"},
		{"fractional second", "* * * * *", "UTC", "2026-01-01T00:00:59.999Z", "2026-01-01T00:01:00Z"},
		{"lists ranges steps", "5,20-30/5 8-10 * * *", "UTC", "2026-01-01T08:20:00Z", "2026-01-01T08:25:00Z"},
		{"wildcard step", "*/15 * * * *", "UTC", "2026-01-01T08:01:00Z", "2026-01-01T08:15:00Z"},
		{"value step", "5/20 * * * *", "UTC", "2026-01-01T08:05:00Z", "2026-01-01T08:25:00Z"},
		{"named months and days", "0 9 * jan-mar mon-fri", "UTC", "2026-01-02T09:00:00Z", "2026-01-05T09:00:00Z"},
		{"case insensitive names", "0 9 * JAN MON", "UTC", "2026-01-02T00:00:00Z", "2026-01-05T09:00:00Z"},
		{"day OR weekday", "0 0 13 * MON", "UTC", "2026-01-12T00:00:00Z", "2026-01-13T00:00:00Z"},
		{"weekday OR day", "0 0 13 * MON", "UTC", "2026-01-13T00:00:00Z", "2026-01-19T00:00:00Z"},
		{"Sunday zero", "0 0 * * 0", "UTC", "2026-01-03T00:00:00Z", "2026-01-04T00:00:00Z"},
		{"hourly", "@hourly", "UTC", "2026-01-01T00:00:00Z", "2026-01-01T01:00:00Z"},
		{"daily", "@daily", "UTC", "2026-01-01T12:00:00Z", "2026-01-02T00:00:00Z"},
		{"midnight", "@midnight", "UTC", "2026-01-01T12:00:00Z", "2026-01-02T00:00:00Z"},
		{"weekly", "@weekly", "UTC", "2026-01-01T00:00:00Z", "2026-01-04T00:00:00Z"},
		{"monthly", "@monthly", "UTC", "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z"},
		{"yearly", "@yearly", "UTC", "2026-01-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"annually", "@annually", "UTC", "2026-01-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"whitespace", " \t0  9 * * * \n", "UTC", "2026-01-01T00:00:00Z", "2026-01-01T09:00:00Z"},
		{"explicit timezone", "@daily", "Asia/Tokyo", "2026-01-01T12:00:00Z", "2026-01-01T15:00:00Z"},
		{"DST gap", "30 2 * * *", "Europe/Berlin", "2026-03-28T01:30:00Z", "2026-03-30T00:30:00Z"},
		{"DST repeat first", "30 2 * * *", "Europe/Berlin", "2026-10-24T00:30:00Z", "2026-10-25T00:30:00Z"},
		{"DST repeat second", "30 2 * * *", "Europe/Berlin", "2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z"},
		{"leap day", "0 0 29 2 *", "UTC", "2026-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			location, err := LoadLocation(tt.timezone)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Parse(tt.expression, location)
			if err != nil {
				t.Fatal(err)
			}
			after := instant(t, tt.after)
			next := s.Next(after)
			if !next.Equal(instant(t, tt.want)) || next.Location() != after.Location() {
				t.Fatalf("next = %v; want %s in caller location", next, tt.want)
			}
		})
	}
}

func TestInvalidCron(t *testing.T) {
	for _, expression := range []string{
		"", " ", "* * * *", "* * * * * *", "0 0 * * * extra",
		"60 * * * *", "* 24 * * *", "0 0 0 * *", "0 0 32 * *",
		"0 0 * 0 *", "0 0 * 13 *", "0 0 * * 7", "0 0 * * MON-SUN",
		"*/0 * * * *", "10-5 * * * *", "0 0 * JANUARY *",
		"0 0 * * ?", "0 0 L * *", "0 0 * * MON#2", "0 0 1W * *",
		", * * * *", ",5 * * * *", "5, * * * *", "5,,10 * * * *",
		"*-5 * * * *", "*-1-2 * * * *", "+5 * * * *", "5/+2 * * * *",
		"@every 1h", "@reboot", "@DAILY", "@daily extra",
		"TZ=UTC", "CRON_TZ=UTC", "TZ=UTC 0 0 * * *", "CRON_TZ=UTC 0 0 * * *",
		"9999999999999999999999999 * * * *", "never-print-this-secret * * * *",
	} {
		t.Run(expression, func(t *testing.T) {
			_, err := Parse(expression, time.UTC)
			if err == nil {
				t.Fatal("accepted invalid cron")
			}
			if strings.Contains(err.Error(), "never-print-this-secret") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestLocations(t *testing.T) {
	for _, name := range []string{"UTC", "Europe/Berlin", "America/New_York", "Asia/Kathmandu", "Etc/GMT+5"} {
		if _, err := LoadLocation(name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"Local", " UTC ", "Europe/Nowhere", "+02:00", "/etc/localtime", "../UTC", "never-print-this-secret", "\x00"} {
		_, err := LoadLocation(name)
		if err == nil || strings.Contains(err.Error(), "never-print-this-secret") {
			t.Fatalf("%q: unsafe or missing error: %v", name, err)
		}
	}
}

func TestLocalDefaultIgnoresCallerTimezone(t *testing.T) {
	// No parallel tests: time.Local is a process-wide default.
	original := time.Local
	time.Local = time.FixedZone("host", 9*60*60)
	t.Cleanup(func() { time.Local = original })
	location, err := LoadLocation("")
	if err != nil || location != time.Local {
		t.Fatalf("local default = %v, %v", location, err)
	}
	for _, loc := range []*time.Location{nil, location} {
		s, err := Parse("@daily", loc)
		if err != nil {
			t.Fatal(err)
		}
		next := s.Next(instant(t, "2026-01-01T12:00:00Z"))
		if !next.Equal(instant(t, "2026-01-01T15:00:00Z")) {
			t.Fatalf("next = %v; want midnight in process local time", next)
		}
	}
}

func TestImpossibleCalendar(t *testing.T) {
	s, err := Parse("0 0 30 2 *", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if next := s.Next(instant(t, "2026-01-01T00:00:00Z")); !next.IsZero() {
		t.Fatalf("impossible calendar next = %v", next)
	}
}
