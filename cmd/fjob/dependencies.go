package main

import (
	"fmt"
)

func validateDependencies(jobs []JobSpec) error {
	byName := make(map[string]JobSpec, len(jobs))
	for _, job := range jobs {
		if job.Name == "" {
			continue
		}
		if _, exists := byName[job.Name]; exists {
			return fmt.Errorf("duplicate job name: %s", job.Name)
		}
		byName[job.Name] = job
	}
	for _, job := range jobs {
		for _, dependency := range job.DependsOn {
			if _, exists := byName[dependency]; !exists {
				return fmt.Errorf("job %q depends on unknown job %q", job.Name, dependency)
			}
			if dependency == job.Name {
				return fmt.Errorf("job %q depends on itself", job.Name)
			}
		}
	}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("dependency cycle detected at job %q", name)
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		for _, dependency := range byName[name].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, name)
		visited[name] = true
		return nil
	}
	for name := range byName {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func dependenciesReady(job JobSpec, results map[string]JobResult, jobsByName map[string]JobSpec) (bool, string) {
	for _, dependency := range job.DependsOn {
		dependencyJob := jobsByName[dependency]
		result, done := results[dependencyJob.ID]
		if !done {
			return false, ""
		}
		if result.ExitCode != 0 {
			return false, dependency
		}
	}
	return true, ""
}