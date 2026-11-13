package config

import (
	"fmt"
	"math"
)

type Media struct {
	Enabled           bool    `yaml:"enabled"`
	PrivateReplies    bool    `yaml:"private_replies"`
	CacheDays         int     `yaml:"cache_days"`
	DailyRequestLimit int     `yaml:"daily_request_limit"`
	ReviewThreshold   float64 `yaml:"review_threshold"`
	NSFWThreshold     float64 `yaml:"nsfw_threshold"`
	MaxFileBytes      int64   `yaml:"max_file_bytes"`
	TimeoutSeconds    int     `yaml:"timeout_seconds"`
}

func defaultMedia() Media {
	return Media{PrivateReplies: true, CacheDays: 30, DailyRequestLimit: 200, ReviewThreshold: .35, NSFWThreshold: .75, MaxFileBytes: 10 << 20, TimeoutSeconds: 30}
}

func (c *Config) validateMedia() error {
	m := c.Media
	if m.Enabled && c.FalKey == "" {
		return fmt.Errorf("FAL_KEY required when media.enabled is true")
	}
	if m.CacheDays < 1 || m.CacheDays > 90 || m.DailyRequestLimit < 0 || m.TimeoutSeconds < 1 || m.TimeoutSeconds > 60 || m.MaxFileBytes < 1024 || m.MaxFileBytes > 20<<20 {
		return fmt.Errorf("invalid media cache, budget, timeout or file limit")
	}
	if math.IsNaN(m.ReviewThreshold) || math.IsNaN(m.NSFWThreshold) || math.IsInf(m.ReviewThreshold, 0) || math.IsInf(m.NSFWThreshold, 0) || m.ReviewThreshold < 0 || m.NSFWThreshold > 1 || m.ReviewThreshold >= m.NSFWThreshold {
		return fmt.Errorf("media thresholds must satisfy 0 <= review_threshold < nsfw_threshold <= 1")
	}
	return nil
}
