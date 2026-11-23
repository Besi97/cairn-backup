package config

import "testing"

func TestMetricsConfig(t *testing.T) {
	for _, tc := range []struct {
		suffix string
		valid  bool
	}{
		{"", true}, {"metrics: {listen: '127.0.0.1:9090'}\n", true}, {"metrics: {listen: '[::1]:9090'}\n", true},
		{"metrics: {}\n", false}, {"metrics: {listen: ':9090'}\n", false}, {"metrics: {listen: 'localhost:9090'}\n", false},
		{"metrics: {listen: '127.0.0.1:0'}\n", false}, {"metrics: {listen: '127.0.0.1:65536'}\n", false}, {"metrics: {listen: '127.0.0.1:9090', extra: true}\n", false},
	} {
		t.Run(tc.suffix, func(t *testing.T) {
			c, err := loadText(t, minimal+tc.suffix)
			if (err == nil) != tc.valid {
				t.Fatalf("config error: %v", err)
			}
			if err == nil && tc.suffix == "" && c.Metrics != nil {
				t.Fatal("enabled by default")
			}
		})
	}
}
