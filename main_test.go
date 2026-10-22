package main

import (
	"testing"
	"time"
)

func TestShortDurationSpellsDefaultsLikeConfig(t *testing.T) {
	for d, want := range map[time.Duration]string{
		168 * time.Hour:  "168h",
		15 * time.Minute: "15m",
		10 * time.Minute: "10m",
		30 * time.Second: "30s",
		90 * time.Second: "1m30s",
		0:                "0s",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
