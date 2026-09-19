package main

import "testing"

func TestColorKeyValueMessageTreatsCommandAsOpaque(t *testing.T) {
	message := "submitted project=demo command=[sh -c echo failing local job\nmarker=\"${ROTARI_BASEDIR}/example-failing-job-marker\"\nif [ ! -f \"${marker}\" ]; then touch \"${marker}\"; exit 1; fi]"
	colored := colorKeyValueMessage(message, func(text string) string { return "G{" + text + "}" })

	want := "G{submitted }G{project}=demoG{ }G{command}=[sh -c echo failing local job\nmarker=\"${ROTARI_BASEDIR}/example-failing-job-marker\"\nif [ ! -f \"${marker}\" ]; then touch \"${marker}\"; exit 1; fi]"
	if colored != want {
		t.Fatalf("colorKeyValueMessage() = %q, want %q", colored, want)
	}
}
