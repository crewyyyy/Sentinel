package config

import (
	"math"
	"testing"
)

func TestMediaDefaultsAndValidation(t *testing.T) {
	c := Defaults()
	if c.Media.Enabled || !c.Media.PrivateReplies || c.Media.CacheDays != 30 || c.Media.DailyRequestLimit != 200 {
		t.Fatal("unsafe defaults")
	}
	if err := c.validateMedia(); err != nil {
		t.Fatal(err)
	}
	c.Media.Enabled = true
	if err := c.validateMedia(); err == nil {
		t.Fatal("enabled without credentials")
	}
	c.FalKey = "fixture"
	if err := c.validateMedia(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Media){func(m *Media) { m.CacheDays = 0 }, func(m *Media) { m.MaxFileBytes = 21 << 20 }, func(m *Media) { m.DailyRequestLimit = -1 }, func(m *Media) { m.TimeoutSeconds = 61 }, func(m *Media) { m.ReviewThreshold = m.NSFWThreshold }, func(m *Media) { m.NSFWThreshold = math.NaN() }, func(m *Media) { m.ReviewThreshold = math.Inf(-1) }} {
		c.Media = defaultMedia()
		change(&c.Media)
		if err := c.validateMedia(); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
