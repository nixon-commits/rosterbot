package progress

import (
	"fmt"
	"io"
	"log"
	"strings"
)

// phaseWeight maps phase names to their completion percentage.
var phaseWeight = map[string]int{
	"Roster":       10,
	"Projections":  30,
	"Recent stats": 40,
	"Pitcher info": 55,
	"Handedness":   70,
	"GS budget":    90,
	"Optimize":     100,
}

// Progress displays expanding-summary progress for the optimize command.
type Progress struct {
	interactive bool
	verbose     bool
	w           io.Writer
	pct         int
}

// New creates a progress display. If interactive is false, falls back to log-style output.
func New(interactive bool, w io.Writer) *Progress {
	return &Progress{interactive: interactive, w: w}
}

// NewVerbose creates a progress display where only Logf produces output.
// Header/Start/Done/Warn/Finish are all no-ops.
func NewVerbose() *Progress {
	return &Progress{verbose: true}
}

// Header prints the persistent header line.
func (p *Progress) Header(projSystem string, dates string, dryRun bool) {
	if p.verbose {
		return
	}
	if p.interactive {
		p.headerInteractive(projSystem, dates, dryRun)
		return
	}
	label := projSystem + " · " + dates
	if dryRun {
		label += " · dry-run"
	}
	log.Printf("%s", label)
}

// Start begins a phase (shows loading indicator in interactive mode).
func (p *Progress) Start(phase string) {
	if p.verbose || !p.interactive {
		return
	}
	p.startInteractive(phase)
}

// Done completes a phase with success detail.
func (p *Progress) Done(phase string, detail string) {
	if p.verbose {
		return
	}
	if p.interactive {
		p.doneInteractive(phase, detail)
		return
	}
	log.Printf("%s: %s", phase, detail)
}

// Warn completes a phase with a warning.
func (p *Progress) Warn(phase string, detail string) {
	if p.verbose {
		return
	}
	if p.interactive {
		p.warnInteractive(phase, detail)
		return
	}
	log.Printf("WARNING: %s: %s", phase, detail)
}

// Finish clears the progress bar and prints the closing separator.
func (p *Progress) Finish() {
	if p.verbose || !p.interactive {
		return
	}
	p.finishInteractive()
}

// Logf prints a log line only in verbose mode. No-op otherwise.
func (p *Progress) Logf(format string, args ...any) {
	if p.verbose {
		log.Printf(format, args...)
	}
}

// --- Interactive mode ---

func (p *Progress) headerInteractive(projSystem string, dates string, dryRun bool) {
	label := projSystem + " · " + dates
	if dryRun {
		label += " · dry-run"
	}
	fmt.Fprintf(p.w, "\n  %s\n", label)
	fmt.Fprintf(p.w, "  ──────────────────────────────────────────\n")
}

func (p *Progress) startInteractive(phase string) {
	p.clearBar()
	fmt.Fprintf(p.w, "  ◌ %-15s loading...\n", phase)
	p.drawBar()
}

func (p *Progress) doneInteractive(phase string, detail string) {
	p.clearBar()
	p.clearPrevLine()
	fmt.Fprintf(p.w, "  ✓ %-15s %s\n", phase, detail)
	if w, ok := phaseWeight[phase]; ok {
		p.pct = w
	}
	p.drawBar()
}

func (p *Progress) warnInteractive(phase string, detail string) {
	p.clearBar()
	p.clearPrevLine()
	fmt.Fprintf(p.w, "  ⚠ %-15s %s\n", phase, detail)
	if w, ok := phaseWeight[phase]; ok {
		p.pct = w
	}
	p.drawBar()
}

func (p *Progress) finishInteractive() {
	p.clearBar()
	fmt.Fprintf(p.w, "  ──────────────────────────────────────────\n")
}

func (p *Progress) drawBar() {
	filled := p.pct / 5 // 20-char bar, each char = 5%
	empty := 20 - filled
	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	fmt.Fprintf(p.w, "  [%s] %d%%\r", bar, p.pct)
}

// barLineWidth is the maximum width of the progress bar line: "  [20-char bar] 100%"
const barLineWidth = 32

func (p *Progress) clearBar() {
	fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", barLineWidth))
}

func (p *Progress) clearPrevLine() {
	fmt.Fprintf(p.w, "\033[A\033[2K")
}
