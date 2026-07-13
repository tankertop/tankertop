package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// sanitize makes bytes that originate from a container — log lines, env, file
// names, file contents, exec error text — safe to print. A hostile container
// can embed terminal escape sequences: an OSC 52 write hijacks the user's
// clipboard (pastejacking), others spoof the window title or redraw the screen
// to forge tankertop's own UI. We strip ANSI escape sequences and drop the
// remaining C0/C1 control bytes. Newlines, tabs and carriage returns are kept
// so the caller's own flattener/escaper can decide how to show them.
func sanitize(s string) string {
	s = ansi.Strip(s)
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isControl(r rune) bool {
	if r == '\n' || r == '\r' || r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// meter renders a labelled horizontal gauge: "CPU [▓▓▓░░░] 48%  detail".
func meter(label string, frac float64, detail string, width int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	barW := width - len(label) - len(detail) - 9
	if barW < 5 {
		barW = 5
	}
	filled := int(frac * float64(barW))
	bar := gradientBar(filled, barW)
	pct := lipgloss.NewStyle().Foreground(gradColor(frac)).Bold(true).Render(fmt.Sprintf("%3.0f%%", frac*100))
	return fmt.Sprintf("%s [%s] %s  %s",
		styText.Render(label), bar, pct, styDim.Render(detail))
}

// resample picks n points spread across vals (nearest-neighbour downsample).
func resample(vals []float64, n int) []float64 {
	if n <= 0 || len(vals) == 0 {
		return nil
	}
	if len(vals) <= n {
		return vals
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		idx := i * (len(vals) - 1) / (n - 1)
		out[i] = vals[idx]
	}
	return out
}

// braille column bit patterns for dot heights 0..4 (bottom-up).
var (
	brailleLeft  = [5]byte{0x00, 0x40, 0x44, 0x46, 0x47}
	brailleRight = [5]byte{0x00, 0x80, 0xa0, 0xb0, 0xb8}
)

// podSpark renders a per-pod CPU sparkline as a single row of braille (2 samples
// per cell, 4 vertical levels). The *shape* is normalised to the pod's own range
// so even small pods show their trend; the colour encodes absolute load. An idle
// pod shows a faint grey baseline (btop-style).
func podSpark(raw []float64, ref float64, width int) string {
	if width < 1 {
		return ""
	}
	if len(raw) == 0 || ref <= 0 {
		return styDim.Render(strings.Repeat("⠤", width))
	}
	n := width * 2
	vals := resample(raw, n)
	if len(vals) < n {
		vals = append(make([]float64, n-len(vals)), vals...)
	}
	mn, mx := vals[0], vals[0]
	for _, v := range vals {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	flat := mx-mn < 1e-9
	height := func(v float64) int {
		var s float64
		if flat {
			s = v / ref
		} else {
			s = (v - mn) / (mx - mn)
		}
		h := int(clamp01(s)*4 + 0.5)
		if h > 4 {
			h = 4
		}
		return h
	}

	var b strings.Builder
	for c := 0; c < width; c++ {
		lv, rv := vals[c*2], vals[c*2+1]
		bits := brailleLeft[height(lv)] | brailleRight[height(rv)]
		vmax := lv
		if rv > vmax {
			vmax = rv
		}
		col := gradColor(clamp01(vmax / ref))
		if bits == 0 {
			bits = 0x40 | 0x80 // grey baseline so the line is always visible
		}
		if vmax/ref < 0.04 {
			col = colDim
		}
		b.WriteString(lipgloss.NewStyle().Foreground(col).Render(string(rune(0x2800 + int(bits)))))
	}
	return b.String()
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// humanBytes formats a byte count compactly (e.g. 3.1G).
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(b)/float64(div), "KMGTPE"[exp])
}

// humanCPU formats milli-cores (e.g. 1200m or 0m).
func humanCPU(milli int64) string {
	return fmt.Sprintf("%dm", milli)
}

// humanAge formats a duration like kubectl (5d, 3h, 12m, 4s).
func humanAge(seconds float64) string {
	s := int64(seconds)
	switch {
	case s >= 86400:
		return fmt.Sprintf("%dd", s/86400)
	case s >= 3600:
		return fmt.Sprintf("%dh", s/3600)
	case s >= 60:
		return fmt.Sprintf("%dm", s/60)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// pad right-pads (or truncates) s to width visible columns.
func pad(s string, width int) string {
	r := []rune(s)
	if len(r) > width {
		if width <= 1 {
			return string(r[:width])
		}
		return string(r[:width-1]) + "…"
	}
	return s + strings.Repeat(" ", width-len(r))
}

// padLeft right-aligns s in width columns, for numbers that share a column.
func padLeft(s string, width int) string {
	r := []rune(s)
	if len(r) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(r)) + s
}

// fit clips (ANSI-aware) and pads s to exactly width visible columns.
// fit truncates s to exactly width display columns, padding with spaces. It is
// the last gate every box body line passes through, so it also flattens
// newlines and tabs: MaxWidth truncates each line but keeps the line breaks, and
// a body line that renders as two rows makes its box one row too tall — which
// silently pushes everything below it off the bottom of the screen. Pod specs
// really do contain env values with trailing newlines.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = flatten(s)
	s = lipgloss.NewStyle().MaxWidth(width).Render(s)
	if w := lipgloss.Width(s); w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}

var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// flatten collapses anything that would start a new terminal row. ANSI escapes
// are left alone.
func flatten(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	return lineBreaks.Replace(s)
}

// brailleDots[inCell][side] gives the braille bit for a dot at vertical
// position inCell (0=top..3=bottom) on the left(0) or right(1) column.
var brailleDots = [4][2]byte{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// brailleGraph renders a series of values (each 0..1) as a high-res braille
// area graph of the given cell width/height. Most recent samples sit at the
// right edge; columns are coloured green→yellow→red by magnitude.
func brailleGraph(vals []float64, width, height int) []string {
	if width < 1 || height < 1 {
		return nil
	}
	dotCols, dotRows := width*2, height*4
	data := make([]float64, dotCols)
	n := len(vals)
	for i := 0; i < dotCols; i++ {
		if idx := n - dotCols + i; idx >= 0 && idx < n {
			data[i] = vals[idx]
		}
	}
	cells := make([][]byte, height)
	for r := range cells {
		cells[r] = make([]byte, width)
	}
	for x := 0; x < dotCols; x++ {
		v := data[x]
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		filled := int(v*float64(dotRows) + 0.5)
		for d := 0; d < filled; d++ {
			dotRow := dotRows - 1 - d
			cr, inCell := dotRow/4, dotRow%4
			cc, side := x/2, x%2
			cells[cr][cc] |= brailleDots[inCell][side]
		}
	}
	lines := make([]string, height)
	for r := 0; r < height; r++ {
		// colour by vertical position: low rows green, high rows red (btop)
		vFrac := 1.0
		if height > 1 {
			vFrac = 1 - (float64(r)+0.5)/float64(height)
		}
		rowCol := gradColor(vFrac)
		var b strings.Builder
		for c := 0; c < width; c++ {
			if cells[r][c] == 0 {
				b.WriteByte(' ')
				continue
			}
			ch := rune(0x2800 + int(cells[r][c]))
			b.WriteString(lipgloss.NewStyle().Foreground(rowCol).Render(string(ch)))
		}
		lines[r] = b.String()
	}
	return lines
}

// gradientBar renders a btop-style meter: the full green→yellow→red gradient is
// always shown across the bar, but only the filled portion is lit — the rest is
// the same gradient heavily darkened.
func gradientBar(filled, width int) string {
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		frac := 0.0
		if width > 1 {
			frac = float64(i) / float64(width-1)
		}
		r, g, bl := gradRGB(frac)
		if i < filled {
			b.WriteString(lipgloss.NewStyle().Foreground(rgb(r, g, bl)).Render("█"))
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(dimRGB(r, g, bl, 0.26)).Render("█"))
		}
	}
	return b.String()
}

// miniMeter renders a compact "[▓▓░░] 42%" gauge of the given bar width.
func miniMeter(frac float64, barW int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	if barW < 2 {
		barW = 2
	}
	filled := int(frac * float64(barW))
	bar := gradientBar(filled, barW)
	return fmt.Sprintf("[%s] %s", bar, lipgloss.NewStyle().Foreground(gradColor(frac)).Render(fmt.Sprintf("%3.0f%%", frac*100)))
}

// box draws a rounded panel with an embedded title and fixed width/height.
func box(title string, body []string, width, height int) string {
	return boxColored(title, body, width, height, colFrame)
}

// boxColored is box with a chosen border colour (used to mark the focused pane).
func boxColored(title string, body []string, width, height int, border lipgloss.Color) string {
	if width < 2 {
		width = 2
	}
	if height < 2 {
		height = 2
	}
	inner := width - 2
	innerH := height - 2
	bs := lipgloss.NewStyle().Foreground(border)

	// Leave room for the leading "╭─" dash, so the title may use at most inner-1.
	maxTitle := inner - 1
	if maxTitle < 0 {
		maxTitle = 0
	}
	t := styTitle.Render(" " + title + " ")
	tw := lipgloss.Width(t)
	if tw > maxTitle {
		t, tw = fit(t, maxTitle), maxTitle
	}
	dash := inner - 1 - tw
	if dash < 0 {
		dash = 0
	}
	top := bs.Render("╭─") + t + bs.Render(strings.Repeat("─", dash)+"╮")
	bottom := bs.Render("╰" + strings.Repeat("─", inner) + "╯")

	side := bs.Render("│")
	rows := make([]string, 0, height)
	rows = append(rows, top)
	for r := 0; r < innerH; r++ {
		line := ""
		if r < len(body) {
			line = body[r]
		}
		rows = append(rows, side+fit(line, inner)+side)
	}
	rows = append(rows, bottom)
	return strings.Join(rows, "\n")
}

// joinRows pairs left/right column line-slices side by side at fixed widths.
func joinRows(left []string, leftW int, right []string, rightW int, gap int) []string {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	out := make([]string, n)
	sp := strings.Repeat(" ", gap)
	for i := 0; i < n; i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = fit(l, leftW) + sp + fit(r, rightW)
	}
	return out
}
