// Package cli provides a professional terminal UI for the kapro CLI.
//
// It wraps lipgloss for styling, spinner for async operations, and provides
// consistent output helpers (tables, status lines, headers, errors) used by
// all kapro commands.
//
// Design principles:
//   - Colors degrade gracefully (NO_COLOR, dumb terminal, pipe to file)
//   - JSON output via -o json bypasses all styling
//   - Spinners only appear on interactive terminals
//   - Every output function is safe to call from any goroutine
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/fatih/color"
)

// Theme defines the color palette used throughout the CLI.
var Theme = struct {
	// Primary colors
	Success lipgloss.Style
	Error   lipgloss.Style
	Warning lipgloss.Style
	Info    lipgloss.Style
	Muted   lipgloss.Style

	// Table
	Header    lipgloss.Style
	Cell      lipgloss.Style
	Separator lipgloss.Style

	// Status phases
	PhaseComplete    lipgloss.Style
	PhaseProgressing lipgloss.Style
	PhaseFailed      lipgloss.Style
	PhasePending     lipgloss.Style
	PhaseWaiting     lipgloss.Style

	// Branding
	Brand lipgloss.Style
	Title lipgloss.Style
}{
	Success: lipgloss.NewStyle().Foreground(lipgloss.Color("10")), // green
	Error:   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),  // red
	Warning: lipgloss.NewStyle().Foreground(lipgloss.Color("11")), // yellow
	Info:    lipgloss.NewStyle().Foreground(lipgloss.Color("12")), // blue
	Muted:   lipgloss.NewStyle().Foreground(lipgloss.Color("8")),  // gray

	Header:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")),
	Cell:      lipgloss.NewStyle(),
	Separator: lipgloss.NewStyle().Foreground(lipgloss.Color("8")),

	PhaseComplete:    lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true),
	PhaseProgressing: lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true),
	PhaseFailed:      lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true),
	PhasePending:     lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	PhaseWaiting:     lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true),

	Brand: lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true),
	Title: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")),
}

// Out is the default output writer. Commands write here instead of os.Stdout
// so tests can capture output.
var Out io.Writer = os.Stdout

// OutputFormat controls whether to render styled text or raw JSON.
var OutputFormat string

// IsJSON returns true when output should be machine-readable JSON.
func IsJSON() bool {
	return OutputFormat == "json"
}

// --- Status output ---

// Success prints a green success message.
func Success(msg string) {
	_, _ = fmt.Fprintln(Out, Theme.Success.Render("  "+msg))
}

// Successf prints a formatted green success message.
func Successf(format string, args ...any) {
	Success(fmt.Sprintf(format, args...))
}

// Error prints a red error message.
func Error(msg string) {
	_, _ = fmt.Fprintln(Out, Theme.Error.Render("  "+msg))
}

// Errorf prints a formatted red error message.
func Errorf(format string, args ...any) {
	Error(fmt.Sprintf(format, args...))
}

// Warn prints a yellow warning message.
func Warn(msg string) {
	_, _ = fmt.Fprintln(Out, Theme.Warning.Render("  "+msg))
}

// Info prints a blue info message.
func Info(msg string) {
	_, _ = fmt.Fprintln(Out, Theme.Info.Render("  "+msg))
}

// Infof prints a formatted blue info message.
func Infof(format string, args ...any) {
	Info(fmt.Sprintf(format, args...))
}

// Muted prints a gray muted message.
func Muted(msg string) {
	_, _ = fmt.Fprintln(Out, Theme.Muted.Render("  "+msg))
}

// --- Headers ---

// Header prints a section header with a horizontal rule.
func Header(title string) {
	fmt.Fprintln(Out)
	_, _ = fmt.Fprintln(Out, Theme.Title.Render("  "+title))
	_, _ = fmt.Fprintln(Out, Theme.Separator.Render("  "+strings.Repeat("─", len(title)+2)))
}

// --- Spinners ---

// Braille dot spinner frames — smooth and modern.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner wraps briandowns/spinner with kapro styling.
// Shows ✔ on success, ✗ on failure, with colors.
type Spinner struct {
	s   *spinner.Spinner
	msg string
}

// NewSpinner creates a styled spinner with the given message.
func NewSpinner(msg string) *Spinner {
	s := spinner.New(spinnerFrames, 80*time.Millisecond)
	s.Suffix = "  " + msg
	_ = s.Color("cyan", "bold")
	s.Writer = os.Stderr
	return &Spinner{s: s, msg: msg}
}

// Start begins the spinner animation.
func (sp *Spinner) Start() {
	if IsJSON() || !isInteractive() {
		fmt.Fprintf(os.Stderr, "  … %s\n", sp.msg)
		return
	}
	sp.s.Start()
}

// Stop stops the spinner and clears the line.
func (sp *Spinner) Stop() {
	if sp.s.Active() {
		sp.s.Stop()
	}
}

// StopWith stops the spinner and prints a final message.
func (sp *Spinner) StopWith(msg string) {
	sp.Stop()
	fmt.Fprintln(os.Stderr, "  "+msg)
}

// StopSuccess stops the spinner with a green ✔ and success message.
func (sp *Spinner) StopSuccess(msg string) {
	sp.Stop()
	green := color.New(color.FgGreen, color.Bold)
	fmt.Fprintf(os.Stderr, "  %s %s\n", green.Sprint("✔"), msg)
}

// StopFail stops the spinner with a red ✗ and error message.
func (sp *Spinner) StopFail(msg string) {
	sp.Stop()
	red := color.New(color.FgRed, color.Bold)
	fmt.Fprintf(os.Stderr, "  %s %s\n", red.Sprint("✗"), msg)
}

// StopWarn stops the spinner with a yellow ⚠ and warning message.
func (sp *Spinner) StopWarn(msg string) {
	sp.Stop()
	yellow := color.New(color.FgYellow, color.Bold)
	fmt.Fprintf(os.Stderr, "  %s %s\n", yellow.Sprint("⚠"), msg)
}

// StopInfo stops the spinner with a blue ℹ and info message.
func (sp *Spinner) StopInfo(msg string) {
	sp.Stop()
	blue := color.New(color.FgCyan, color.Bold)
	fmt.Fprintf(os.Stderr, "  %s %s\n", blue.Sprint("ℹ"), msg)
}

// Update changes the spinner suffix message.
func (sp *Spinner) Update(msg string) {
	sp.msg = msg
	sp.s.Suffix = "  " + msg
}

// --- Tables ---

// Table renders a formatted table with headers and rows.
type Table struct {
	headers []string
	rows    [][]string
	widths  []int
}

// NewTable creates a table with the given headers.
func NewTable(headers ...string) *Table {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	return &Table{
		headers: headers,
		widths:  widths,
	}
}

// AddRow adds a row to the table.
func (t *Table) AddRow(cells ...string) {
	// Pad or truncate to match header count.
	row := make([]string, len(t.headers))
	for i := range row {
		if i < len(cells) {
			row[i] = cells[i]
		}
		if len(row[i]) > t.widths[i] {
			t.widths[i] = len(row[i])
		}
	}
	t.rows = append(t.rows, row)
}

// Render prints the table to Out.
func (t *Table) Render() {
	if len(t.rows) == 0 {
		Muted("(no results)")
		return
	}

	fmt.Fprintln(Out)

	// Header row.
	headerLine := "  "
	for i, h := range t.headers {
		headerLine += Theme.Header.Render(pad(h, t.widths[i]))
		if i < len(t.headers)-1 {
			headerLine += "  "
		}
	}
	_, _ = fmt.Fprintln(Out, headerLine)

	// Separator.
	sepLine := "  "
	for i, w := range t.widths {
		sepLine += Theme.Separator.Render(strings.Repeat("─", w))
		if i < len(t.widths)-1 {
			sepLine += "  "
		}
	}
	_, _ = fmt.Fprintln(Out, sepLine)

	// Data rows.
	for _, row := range t.rows {
		line := "  "
		for i, cell := range row {
			styled := styleCell(t.headers[i], cell)
			// Pad after styling (use raw cell length for padding calculation).
			padding := t.widths[i] - len(cell)
			if padding < 0 {
				padding = 0
			}
			line += styled + strings.Repeat(" ", padding)
			if i < len(row)-1 {
				line += "  "
			}
		}
		_, _ = fmt.Fprintln(Out, line)
	}
	fmt.Fprintln(Out)
}

// --- Key-Value pairs ---

// KV prints a key-value pair with aligned formatting.
func KV(key, value string) {
	k := Theme.Muted.Render(fmt.Sprintf("  %-16s", key))
	_, _ = fmt.Fprintf(Out, "%s %s\n", k, value)
}

// --- JSON output ---

// JSON prints the value as indented JSON (for -o json mode).
func JSON(v any) error {
	enc := json.NewEncoder(Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- Phase styling ---

// StyledPhase returns a phase string with appropriate color.
func StyledPhase(phase string) string {
	switch phase {
	case "Complete", "Converged":
		return Theme.PhaseComplete.Render(phase)
	case "Progressing", "Applying", "Soaking", "HealthCheck", "Verification", "MetricsCheck":
		return Theme.PhaseProgressing.Render(phase)
	case "Failed":
		return Theme.PhaseFailed.Render(phase)
	case "Pending":
		return Theme.PhasePending.Render(phase)
	case "WaitingApproval":
		return Theme.PhaseWaiting.Render(phase)
	default:
		return phase
	}
}

// --- Helpers ---

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func styleCell(header, cell string) string {
	switch header {
	case "PHASE", "STATUS":
		return StyledPhase(cell)
	case "RELEASE", "NAME":
		return Theme.Info.Render(cell)
	case "TARGET", "CLUSTER":
		return color.New(color.FgWhite, color.Bold).Sprint(cell)
	default:
		return cell
	}
}

func isInteractive() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("CI") != "" {
		return false
	}
	f, ok := Out.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// Age formats a duration as a human-readable age string (e.g. "5m", "2h", "3d").
func Age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
