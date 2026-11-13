package borg

import (
	"reflect"
	"testing"
)

func TestLockWaitArgs(t *testing.T) {
	for _, tt := range []struct {
		seconds *int
		want    []string
	}{
		{nil, []string{"--lock-wait", "60"}},
		{newInt(0), []string{"--lock-wait", "0"}},
		{newInt(12), []string{"--lock-wait", "12"}},
	} {
		got, err := LockWaitArgs(tt.seconds)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("args = %q, error = %v", got, err)
		}
	}
	if _, err := LockWaitArgs(newInt(-1)); err == nil {
		t.Fatal("negative lock wait accepted")
	}
}

func newInt(n int) *int { return &n }
