package main

import "testing"

func TestDependenciesReady(t *testing.T) {
	dependency := JobSpec{ID: "dependency", Name: "build"}
	job := JobSpec{ID: "test", Name: "test", DependsOn: []string{"build"}}
	jobsByName := map[string]JobSpec{"build": dependency}

	tests := []struct {
		name         string
		results      map[string]JobResult
		wantReady    bool
		wantFailedBy string
	}{
		{name: "dependency is unfinished", results: map[string]JobResult{}, wantReady: false},
		{name: "dependency succeeds", results: map[string]JobResult{"dependency": {ID: "dependency", ExitCode: 0}}, wantReady: true},
		{name: "dependency fails", results: map[string]JobResult{"dependency": {ID: "dependency", ExitCode: 1}}, wantReady: false, wantFailedBy: "build"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ready, failedBy := dependenciesReady(job, test.results, jobsByName)
			if ready != test.wantReady || failedBy != test.wantFailedBy {
				t.Fatalf("dependenciesReady() = (%t, %q), want (%t, %q)", ready, failedBy, test.wantReady, test.wantFailedBy)
			}
		})
	}
}

func TestDependenciesReadyRequiresAllDependencies(t *testing.T) {
	job := JobSpec{ID: "test", Name: "test", DependsOn: []string{"build", "lint"}}
	jobsByName := map[string]JobSpec{
		"build": {ID: "build", Name: "build"},
		"lint":  {ID: "lint", Name: "lint"},
	}
	results := map[string]JobResult{
		"build": {ID: "build", ExitCode: 0},
	}

	ready, failedBy := dependenciesReady(job, results, jobsByName)
	if ready || failedBy != "" {
		t.Fatalf("dependenciesReady() = (%t, %q), want (false, empty)", ready, failedBy)
	}
}
