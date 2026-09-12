package main

import "os"

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
