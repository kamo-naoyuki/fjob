package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiWhite  = "\033[37m"
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
func white(text string) string  { return colorText(text, ansiWhite, os.Stdout) }

func colorLabeledDetails(details string, failed bool) string {
	lines := strings.SplitAfter(details, "\n")
	failedJobOutput := failed
	for i, line := range lines {
		text := strings.TrimSuffix(line, "\n")
		if text == "" {
			if !failed {
				failedJobOutput = false
			}
			lines[i] = newline(line)
			continue
		}
		labelEnd := strings.IndexByte(text, ':')
		if labelEnd < 0 {
			lines[i] = white(text) + newline(line)
			continue
		}
		label := text[:labelEnd+1]
		if strings.TrimSpace(label) == "Failed job output:" {
			failedJobOutput = true
		}
		labelColor := cyan
		if failedJobOutput {
			labelColor = red
		}
		lines[i] = labelColor(label) + white(text[labelEnd+1:]) + newline(line)
	}
	return strings.Join(lines, "")
}

// printError writes a red-colored error line to stderr.
func printError(a ...any) {
	fmt.Fprintln(os.Stderr, red(fmt.Sprint(a...)))
}

// printErrorf formats and writes a red-colored error line to stderr.
func printErrorf(format string, a ...any) {
	fmt.Fprintln(os.Stderr, red(fmt.Sprintf(format, a...)))
}

func colorMessage(message string) string {
	lines := strings.SplitAfter(message, "\n")
	failedJobOutput := false
	for i, line := range lines {
		text := strings.TrimSuffix(line, "\n")
		if text == "" {
			failedJobOutput = false
		}
		switch {
		case strings.HasPrefix(text, "Run failed:"):
			lines[i] = red(text) + newline(line)
		case strings.HasPrefix(text, "Run finished:"):
			lines[i] = green(text) + newline(line)
		case strings.HasPrefix(text, "Retrying job:"):
			lines[i] = yellow(text) + newline(line)
		case strings.HasPrefix(text, "Failed job output:"):
			failedJobOutput = true
			lines[i] = red(text) + newline(line)
		case strings.HasPrefix(text, "Run started"), strings.HasPrefix(text, "Inspect"), strings.HasPrefix(text, "Check"), strings.HasPrefix(text, "Cancel"), strings.HasPrefix(text, "Rerun"), strings.HasPrefix(text, "Job running:"):
			lines[i] = cyan(text) + newline(line)
		case strings.Contains(text, ":"):
			labelEnd := strings.IndexByte(text, ':')
			label := text[:labelEnd+1]
			if failedJobOutput {
				lines[i] = red(label) + white(text[labelEnd+1:]) + newline(line)
			} else {
				lines[i] = cyan(label) + white(text[labelEnd+1:]) + newline(line)
			}
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
