package tui

import "strings"

// truncate shortens s to at most n runes, appending … if needed.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "\u2026"
}

// padToHeight pads content with blank lines so it occupies exactly height lines.
func padToHeight(content string, height int) string {
	lines := strings.Split(content, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// windowLines slices a list of display lines to maxLines, keeping the cursor
// line roughly centered when scrolling is needed.
func windowLines(lines []string, cursorLine, maxLines int) []string {
	if len(lines) <= maxLines {
		out := lines
		for len(out) < maxLines {
			out = append(out, "")
		}
		return out
	}

	half := maxLines / 2
	start := 0
	switch {
	case cursorLine < half:
		start = 0
	case cursorLine >= len(lines)-half:
		start = len(lines) - maxLines
	default:
		start = cursorLine - half
	}
	if start < 0 {
		start = 0
	}

	end := start + maxLines
	if end > len(lines) {
		end = len(lines)
	}
	return lines[start:end]
}
