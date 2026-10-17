package domain

import (
	"fmt"
	"strconv"
	"time"
)

// Money is an amount in minor units (cents). Integer arithmetic keeps premiums
// reproducible to the cent, which floating point cannot guarantee.
type Money struct {
	Currency string
	Minor    int64
}

func (m Money) String() string {
	sign, v := "", m.Minor
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s %s%d.%02d", m.Currency, sign, v/100, v%100)
}

// basisPoints is the fixed-point scale for multipliers and rates: 10000 = 1.0.
const basisPoints = 10000

// applyBP multiplies a non-negative amount by a basis-point factor and rounds
// half up to the nearest minor unit.
func applyBP(amount, bp int64) int64 {
	return (amount*bp + basisPoints/2) / basisPoints
}

func formatBP(bp int64) string {
	return strconv.FormatFloat(float64(bp)/basisPoints, 'f', -1, 64)
}

const dateLayout = "2006-01-02"

// ParseDate reads a calendar date (YYYY-MM-DD) as midnight UTC.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, invalid("invalid date", []Issue{{Field: "date", Message: "expected YYYY-MM-DD, got " + strconv.Quote(s)}})
	}
	return t, nil
}

func FormatDate(t time.Time) string {
	return t.Format(dateLayout)
}

// Today truncates an instant to its UTC calendar date.
func Today(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
