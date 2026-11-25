package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronField is one parsed field of a cron expression.
type cronField struct {
	// any is true only for a bare "*": it is what decides whether the day
	// fields combine as AND or OR.
	any bool
	set map[int]bool
}

func (f cronField) match(v int) bool {
	if f.any {
		return true
	}
	return f.set[v]
}

// cronExpression is a five-field cron expression, parsed and validated.
type cronExpression struct {
	minute, hour, dom, month, dow cronField
	raw                           string
}

// parseCron parses the standard five fields: minute, hour, day of month, month,
// day of week. It accepts "*", numbers, ranges, steps and comma lists; names
// are refused rather than guessed at, because a typo in a name would silently
// move a job.
func parseCron(raw string) (cronExpression, error) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) != 5 {
		return cronExpression{}, fmt.Errorf("cron %q: want five fields, got %d", raw, len(fields))
	}

	minute, err := parseField(fields[0], 0, 59)
	if err != nil {
		return cronExpression{}, fmt.Errorf("cron %q: minute: %w", raw, err)
	}
	hour, err := parseField(fields[1], 0, 23)
	if err != nil {
		return cronExpression{}, fmt.Errorf("cron %q: hour: %w", raw, err)
	}
	dom, err := parseField(fields[2], 1, 31)
	if err != nil {
		return cronExpression{}, fmt.Errorf("cron %q: day of month: %w", raw, err)
	}
	month, err := parseField(fields[3], 1, 12)
	if err != nil {
		return cronExpression{}, fmt.Errorf("cron %q: month: %w", raw, err)
	}
	dow, err := parseField(fields[4], 0, 7)
	if err != nil {
		return cronExpression{}, fmt.Errorf("cron %q: day of week: %w", raw, err)
	}
	// 7 is Sunday in the traditional spelling; 0 already is.
	if dow.set[7] {
		dow.set[0] = true
		delete(dow.set, 7)
	}

	return cronExpression{minute: minute, hour: hour, dom: dom, month: month, dow: dow, raw: raw}, nil
}

// parseField parses one field: a comma-separated list of "*", "n", "a-b", each
// of which may carry a "/step" suffix.
func parseField(raw string, min, max int) (cronField, error) {
	f := cronField{set: map[int]bool{}}
	if raw == "*" {
		f.any = true
	}
	for _, part := range strings.Split(raw, ",") {
		if part == "" {
			return cronField{}, fmt.Errorf("empty list entry")
		}

		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n <= 0 || strconv.Itoa(n) != stepText {
				return cronField{}, fmt.Errorf("step %q is not a positive number", stepText)
			}
			if !strings.Contains(base, "*") && !strings.Contains(base, "-") {
				// "5/10" is refused rather than read as "5, 15, 25": the two
				// readings in the wild disagree, and a schedule that fires at
				// the wrong minutes is worse than one that will not save.
				return cronField{}, fmt.Errorf("step on the bare value %q: use * or a range before /", base)
			}
			step = n
		}

		lo, hi := min, max
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			left, right, _ := strings.Cut(base, "-")
			a, err := strconv.Atoi(left)
			if err != nil {
				return cronField{}, fmt.Errorf("%q is not a number", left)
			}
			b, err := strconv.Atoi(right)
			if err != nil {
				return cronField{}, fmt.Errorf("%q is not a number", right)
			}
			lo, hi = a, b
		default:
			n, err := strconv.Atoi(base)
			if err != nil {
				return cronField{}, fmt.Errorf("%q is not a number", base)
			}
			lo, hi = n, n
		}

		if lo < min || hi > max || lo > hi {
			return cronField{}, fmt.Errorf("range %d-%d is outside %d-%d", lo, hi, min, max)
		}
		for v := lo; v <= hi; v += step {
			f.set[v] = true
		}
	}
	return f, nil
}

// nextCron returns the first instant strictly after after whose local wall clock
// matches expr. The search is bounded to a year: an expression that matches
// nothing inside it (29 February in a common year, say) is reported rather than
// searched forever.
func nextCron(expr cronExpression, loc *time.Location, after time.Time) (time.Time, error) {
	cursor := after.UTC().Truncate(time.Minute).Add(time.Minute)

	// The bound is a year of minutes. Checking every minute is what makes the
	// day-of-week and day-of-month rule, and DST's missing and repeated hours,
	// fall out without special cases.
	limit := 366 * 24 * 60
	for i := 0; i < limit; i++ {
		local := cursor.In(loc)
		if expr.matches(local) {
			return cursor.In(loc), nil
		}
		cursor = cursor.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron %q matches no time in the next year", expr.raw)
}

// matches reports whether one instant's local wall clock fits the expression.
func (e cronExpression) matches(t time.Time) bool {
	if !e.minute.match(t.Minute()) || !e.hour.match(t.Hour()) || !e.month.match(int(t.Month())) {
		return false
	}

	domMatches := e.dom.match(t.Day())
	dowMatches := e.dow.match(int(t.Weekday()))
	if e.dom.any || e.dow.any {
		// One field is unrestricted, so it cannot widen the other: AND is the
		// only reading that keeps a restricted field meaningful.
		return domMatches && dowMatches
	}
	// Both are restricted. The traditional rule is OR, not AND.
	return domMatches || dowMatches
}
