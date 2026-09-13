package main

import (
	"os"
	"strings"
)

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

func colorText(text, color string, file *os.File) string {
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return text
	}
	return color + text + ansiReset
}

func green(text string) string  { return colorText(text, ansiGreen, os.Stdout) }
func red(text string) string    { return colorText(text, ansiRed, os.Stderr) }
func yellow(text string) string { return colorText(text, ansiYellow, os.Stdout) }
func cyan(text string) string   { return colorText(text, ansiCyan, os.Stdout) }

func colorMessage(message string) string {
	lines := strings.SplitAfter(message, "\n")
	for i, line := range lines {
		text := strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(text, "Run failed:"):
			lines[i] = red(text) + newline(line)
		case strings.HasPrefix(text, "Run finished:"):
			lines[i] = green(text) + newline(line)
		case strings.HasPrefix(text, "Retrying job:"):
			lines[i] = yellow(text) + newline(line)
		case strings.HasPrefix(text, "Run started"), strings.HasPrefix(text, "Inspect"), strings.HasPrefix(text, "Check"), strings.HasPrefix(text, "Cancel"), strings.HasPrefix(text, "Rerun"), strings.HasPrefix(text, "Failed job output"):
			lines[i] = cyan(text) + newline(line)
		}
	}
	return strings.Join(lines, "")
}

func newline(line string) string {
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return ""
}
