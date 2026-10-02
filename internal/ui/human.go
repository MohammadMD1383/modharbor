package ui

import "fmt"

// Units used by HumanBytes and HumanRate.
const (
	ByteUnit = 1
	KiloByte = 1000
	MegaByte = 1000 * KiloByte
	GigaByte = 1000 * MegaByte
	TeraByte = 1000 * GigaByte
)

// HumanBytes renders a byte count using SI units, e.g. "13.5 MB".
func HumanBytes(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "kMGTP"[exp])
}

// HumanCount renders an integer with thin thousands separators.
func HumanCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// HumanRate renders a transfer speed, e.g. "4.2 MB/s".
func HumanRate(bps float64) string {
	return HumanBytes(int64(bps)) + "/s"
}

// HumanDuration renders a coarse duration, e.g. "1h 04m" or "12.3s".
func HumanDuration(seconds float64) string {
	if seconds < 1 {
		return fmt.Sprintf("%.0fms", seconds*1000)
	}
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	m := int(seconds) / 60
	s := int(seconds) % 60
	if m < 60 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	h := m / 60
	m = m % 60
	return fmt.Sprintf("%dh %02dm", h, m)
}

// Plural returns singular when n == 1 and plural otherwise.
func Plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
