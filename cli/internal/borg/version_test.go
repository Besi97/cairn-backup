package borg

import "testing"

func TestSupportedVersion(t *testing.T) {
	for _, version := range []string{"borg 1.2.8", "borg 1.2.9", "borg 1.4.5", "borg 1.10.0", "borg 1.4.5\n"} {
		if err := supportedVersion(version); err != nil {
			t.Errorf("%q: %v", version, err)
		}
	}
	for _, version := range []string{"", "borg 1.1.18", "borg 1.2.7", "borg 2.0.0", "borg 2.0.0b1", "borg 1.5.0rc1", "borg 1.4.5.dev1", "borg 1.4", "unexpected", "borg 1.99999999999999999999999999999.0", "borg 1.4.5\nextra"} {
		if err := supportedVersion(version); err == nil {
			t.Errorf("accepted %q", version)
		}
	}
}
