// Package cronspec parses cron schedules and computes their next run time. It is
// a thin, pure wrapper over robfig/cron's parser supporting standard 5-field
// specs (minute hour dom month dow), @descriptors (@hourly, @daily, …) and the
// @every <duration> form. Kept separate from the cron Store/Scheduler so the
// schedule math is unit-testable without the agent/sqlite dependencies.
package cronspec

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// parser accepts 5-field specs plus @descriptors and @every.
var parser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Parse compiles a cron spec into a schedule.
func Parse(spec string) (cron.Schedule, error) {
	s, err := parser.Parse(strings.TrimSpace(spec))
	if err != nil {
		return nil, fmt.Errorf("invalid cron spec %q: %w", spec, err)
	}
	return s, nil
}

// Valid reports whether spec is a parseable cron schedule.
func Valid(spec string) error {
	_, err := Parse(spec)
	return err
}

// Next returns the first activation strictly after the given time.
func Next(spec string, after time.Time) (time.Time, error) {
	s, err := Parse(spec)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(after), nil
}
