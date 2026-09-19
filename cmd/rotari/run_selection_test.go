package main

import "testing"

func TestResultSelectionMatches(t *testing.T) {
	tests := []struct {
		name      string
		selection string
		finished  bool
		exitCode  int
		want      bool
	}{
		{name: "failed result", selection: "failed", finished: true, exitCode: 1, want: true},
		{name: "successful result is not failed", selection: "failed", finished: true, exitCode: 0, want: false},
		{name: "unfinished result", selection: "unfinished", finished: false, exitCode: 0, want: true},
		{name: "unfinished failed result is not success", selection: "success", finished: false, exitCode: 1, want: false},
		{name: "combined filters", selection: "failed,unfinished", finished: false, exitCode: 0, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resultSelectionMatches(test.selection, test.finished, test.exitCode); got != test.want {
				t.Fatalf("resultSelectionMatches(%q, %t, %d) = %t, want %t", test.selection, test.finished, test.exitCode, got, test.want)
			}
		})
	}
}

func TestAggregatedJobResultForArray(t *testing.T) {
	array := &ArraySpec{First: 1, Last: 3}

	tests := []struct {
		name     string
		results  map[string]JobResult
		wantDone bool
		wantExit int
	}{
		{
			name: "unfinished until every task has a result",
			results: map[string]JobResult{
				"job-1": {ID: "job-1", ExitCode: 0},
				"job-2": {ID: "job-2", ExitCode: 0},
			},
			wantDone: false,
		},
		{
			name: "failed when any task fails",
			results: map[string]JobResult{
				"job-1": {ID: "job-1", ExitCode: 0},
				"job-2": {ID: "job-2", ExitCode: 7},
				"job-3": {ID: "job-3", ExitCode: 0},
			},
			wantDone: true,
			wantExit: 7,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, done := aggregatedJobResult("job", array, test.results)
			if done != test.wantDone || result.ExitCode != test.wantExit {
				t.Fatalf("aggregatedJobResult() = (exit=%d, done=%t), want (exit=%d, done=%t)", result.ExitCode, done, test.wantExit, test.wantDone)
			}
		})
	}
}
