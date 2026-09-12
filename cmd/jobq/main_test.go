package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestResolveQueueNamePriority(t *testing.T) {
	const envName = "JOBQ_QUEUE_NAME"
	old, existed := os.LookupEnv(envName)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(envName, old)
		} else {
			_ = os.Unsetenv(envName)
		}
	})

	if err := os.Setenv(envName, "from-env"); err != nil {
		t.Fatal(err)
	}
	if got := resolveQueueName("from-option"); got != "from-option" {
		t.Fatalf("option priority: got %q", got)
	}
	if got := resolveQueueName(""); got != "from-env" {
		t.Fatalf("environment priority: got %q", got)
	}
	if err := os.Unsetenv(envName); err != nil {
		t.Fatal(err)
	}
	if got := resolveQueueName(""); got != defaultQueueName {
		t.Fatalf("default priority: got %q, want %q", got, defaultQueueName)
	}
}

func TestSplitShellWords(t *testing.T) {
	got, err := splitShellWords(`-p "short queue" --constraint='fast\ node' --exclusive`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "short queue", "--constraint=fast\\ node", "--exclusive"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("word %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitShellWordsRejectsUnterminatedInput(t *testing.T) {
	for _, input := range []string{`"unterminated`, `trailing\`} {
		if _, err := splitShellWords(input); err == nil {
			t.Errorf("splitShellWords(%q) returned nil error", input)
		}
	}
}

func TestParseSlurmExitCode(t *testing.T) {
	cases := map[string]int{"0:0": 0, "1:0": 1, "2:15": 2, "invalid": 1}
	for input, want := range cases {
		if got := parseSlurmExitCode(input); got != want {
			t.Errorf("parseSlurmExitCode(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestMakeRunIDIsUUIDv4(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	first := makeRunID()
	if !pattern.MatchString(first) {
		t.Fatalf("makeRunID() = %q, not UUID v4", first)
	}
	if first == makeRunID() {
		t.Fatal("makeRunID returned the same ID twice")
	}
}

func TestQueueUnmarshalSupportsLegacyCommands(t *testing.T) {
	var queue Queue
	if err := json.Unmarshal([]byte(`{"commands":[["echo","hello"]]}`), &queue); err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || len(queue.Commands[0].Command) != 2 {
		t.Fatalf("unexpected queue: %#v", queue)
	}
	if queue.Commands[0].Command[0] != "echo" {
		t.Fatalf("unexpected command: %#v", queue.Commands[0].Command)
	}
}
