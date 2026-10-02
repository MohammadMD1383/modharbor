package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Spinner is a single-line braille progress indicator. It disables itself
// automatically when stderr is not a terminal.
type Spinner struct {
	label  string
	final  string
	w      io.Writer
	frames []rune
	mu     sync.Mutex
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// NewSpinner builds a spinner bound to stderr.
func NewSpinner(label string) *Spinner {
	return &Spinner{
		label:  label,
		w:      os.Stderr,
		frames: spinnerFrames,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start begins animating.
func (s *Spinner) Start() *Spinner {
	if s == nil {
		return s
	}
	if !ColorEnabled() || !IsTerminalFile(s.w) {
		close(s.done)
		return s
	}
	go func() {
		defer close(s.done)
		i := 0
		t := time.NewTicker(85 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.mu.Lock()
				frame := string(s.frames[i%len(s.frames)])
				label := s.label
				s.mu.Unlock()
				i++
				_, _ = fmt.Fprintf(s.w, "\r\033[2K%s %s", paint(Palette.Brand, frame), label)
			}
		}
	}()
	return s
}

// Update changes the spinner label in place.
func (s *Spinner) Update(format string, a ...any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.label = fmt.Sprintf(format, a...)
	s.mu.Unlock()
}

// Done stops the spinner and prints a final line in its place. When msg is
// empty the line is cleared entirely.
func (s *Spinner) Done(msg string) {
	if s == nil {
		return
	}
	s.Stop()
	if msg == "" {
		return
	}
	_, _ = fmt.Fprintf(s.w, "\r\033[2K%s\n", msg)
}

// Stop halts animation and clears the spinner line.
func (s *Spinner) Stop() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		select {
		case <-s.done:
			// Already disabled or finished.
		default:
			close(s.stop)
			<-s.done
		}
		if ColorEnabled() && IsTerminalFile(s.w) {
			_, _ = fmt.Fprintf(s.w, "\r\033[2K")
		}
	})
}

func IsTerminalFile(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && IsTerminal(f)
}

// ─── Progress bar ───────────────────────────────────────────────────────────

// Progress is a determinate progress bar with a label and byte counter.
type Progress struct {
	total   int64
	current int64
	label   string
	detail  string
	width   int
	started time.Time
	lastLen int
	mu      sync.Mutex
	w       io.Writer
	enabled bool
}

// NewProgress builds a progress bar for a transfer of total bytes.
func NewProgress(label string, total int64) *Progress {
	w := Width() - 34
	if w < 12 {
		w = 12
	}
	if w > 44 {
		w = 44
	}
	return &Progress{
		total:   total,
		label:   label,
		width:   w,
		started: time.Now(),
		w:       os.Stderr,
		enabled: ColorEnabled() && IsTerminal(os.Stderr),
	}
}

// SetTotal updates the expected total (used when Content-Length is unknown).
func (p *Progress) SetTotal(total int64) { p.mu.Lock(); p.total = total; p.mu.Unlock() }

// SetDetail updates the trailing detail text (e.g. "sodium 3.1/13.5 MB").
func (p *Progress) SetDetail(d string) { p.mu.Lock(); p.detail = d; p.mu.Unlock() }

// SetLabel changes the leading label.
func (p *Progress) SetLabel(l string) { p.mu.Lock(); p.label = l; p.mu.Unlock() }

// Add advances the bar by n bytes.
func (p *Progress) Add(n int64) {
	p.mu.Lock()
	p.current += n
	p.renderLocked()
	p.mu.Unlock()
}

// Done finishes the bar and prints a permanent summary line.
func (p *Progress) Done(summary string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		if summary != "" {
			_, _ = fmt.Fprintln(p.w, summary)
		}
		return
	}
	_, _ = fmt.Fprintf(p.w, "\r\033[2K")
	if summary != "" {
		_, _ = fmt.Fprintln(p.w, summary)
	}
}

// Render draws one frame (used internally and by Add).
func (p *Progress) renderLocked() {
	if !p.enabled {
		return
	}
	frac := 0.0
	if p.total > 0 {
		frac = float64(p.current) / float64(p.total)
		if frac > 1 {
			frac = 1
		}
	}
	filled := int(frac * float64(p.width))
	bar := strings.Repeat(barFull, filled)
	remain := p.width - filled
	if remain > 0 {
		bar += strings.Repeat(barEmpty, remain)
	}
	pct := fmt.Sprintf("%3.0f%%", frac*100)
	speed := ""
	if el := time.Since(p.started).Seconds(); el > 0.15 {
		speed = fmt.Sprintf(" %s/s", HumanRate(float64(p.current)/el))
	}
	right := p.detail
	if right == "" {
		right = fmt.Sprintf("%s/%s%s", HumanBytes(p.current), HumanBytes(p.total), speed)
	}
	line := fmt.Sprintf("%s %s %s %s",
		Truncate(p.label, 22),
		paint(Palette.OK, bar),
		paint(Palette.Muted, pct),
		paint(Palette.Faint, right),
	)
	// Overwrite any leftovers from a longer previous frame.
	pad := ""
	if n := p.lastLen - VisibleWidth(line); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	p.lastLen = VisibleWidth(line)
	_, _ = fmt.Fprintf(p.w, "\r\033[2K%s%s", line, pad)
}

// ─── Indented task lines ────────────────────────────────────────────────────

// Task renders one line of a multi-step operation, aligned so results line up.
//
// The detail column is separated by at least one space even when the name is
// wider than the alignment column, so long file names cannot run into it.
func Task(state, name, detail string) {
	s := "  " + Faint(SymDot)
	switch state {
	case "ok":
		s = "  " + paint(Palette.OK, SymOK)
	case "fail":
		s = "  " + paint(Palette.Err, SymFail)
	case "warn":
		s = "  " + paint(Palette.Warn, SymWarn)
	case "skip":
		s = "  " + paint(Palette.Faint, SymSkip)
	case "busy":
		s = "  " + paint(Palette.Brand, SymPending)
	case "add":
		s = "  " + paint(Palette.OK, SymPlus)
	case "del":
		s = "  " + paint(Palette.Err, SymMinus)
	case "fix":
		s = "  " + paint(Palette.Warn, SymFix)
	case "move":
		s = "  " + paint(Palette.Accent, SymMove)
	}

	gap := TaskNameWidth - VisibleWidth(name)
	if gap < 1 {
		gap = 1
	}
	Line(s + " " + name + strings.Repeat(" ", gap) + paint(Palette.Faint, detail))
}

// TaskNameWidth is the alignment column used by Task.
const TaskNameWidth = 30
