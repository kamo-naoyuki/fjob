package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestPrintVersionWritesVersionToStdout(t *testing.T) {
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	printVersion()
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "rotari "+version) {
		t.Fatalf("printVersion output = %q, want it to contain %q", output, "rotari "+version)
	}
}
