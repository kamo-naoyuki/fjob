package main

import (
	"flag"
	"time"
)

type cliFlagSpec struct {
	Name        string
	Description string
	ValueName   string
	Values      []string
}

type cliSubcommandSpec struct {
	Name        string
	Description string
}

type cliCommandSpec struct {
	Name          string
	Description   string
	Usage         string
	Flags         []cliFlagSpec
	Subcommands   []cliSubcommandSpec
	HasPositional bool
}

var cliShortFlagNames = map[string]string{
	"basedir":      "b",
	"project-name": "p",
	"run-id":       "r",
	"job-id":       "j",
	"executor":     "e",
}

func commonCLIFlags() []cliFlagSpec {
	return []cliFlagSpec{
		{Name: "basedir", Description: "state directory", ValueName: "DIR"},
		{Name: "project-name", Description: "project name", ValueName: "NAME"},
	}
}

var cliCommandSpecs = []cliCommandSpec{
	{
		Name:        "check",
		Description: "check queue and dependencies",
		Usage:       "rotari check [--basedir DIR] [--project-name NAME] [--server] [--recover keep|discard]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "server", Description: "also require a running server"},
			cliFlagSpec{Name: "recover", Description: "recover an interrupted run without prompting", ValueName: "keep|discard", Values: []string{"keep", "discard"}},
		),
	},
	{
		Name:  "cancel",
		Usage: "rotari cancel [--basedir DIR] [--project-name NAME] [--job-id ID] [--wait]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "job-id", Description: "cancel a running job; may be repeated", ValueName: "ID"},
			cliFlagSpec{Name: "wait", Description: "wait until cancellation is complete"},
		),
	},
	{
		Name:        "suspend",
		Description: "suspend running jobs",
		Usage:       "rotari suspend [--basedir DIR] [--project-name NAME] [--job-id ID]...",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "job-id", Description: "suspend a running job; may be repeated", ValueName: "ID"},
		),
	},
	{
		Name:        "resume",
		Description: "resume suspended jobs",
		Usage:       "rotari resume [--basedir DIR] [--project-name NAME] [--job-id ID]...",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "job-id", Description: "resume a suspended job; may be repeated", ValueName: "ID"},
		),
	},
	{
		Name:        "delete",
		Description: "delete saved run history",
		Usage:       "rotari delete [--basedir DIR] [--project-name NAME] [--run-id ID]",
		Flags:       append(commonCLIFlags(), cliFlagSpec{Name: "run-id", Description: "delete only the specified run", ValueName: "ID"}),
	},
	{
		Name:        "unlock",
		Description: "remove a confirmed stale run lock",
		Usage:       "rotari unlock [--basedir DIR] [--project-name NAME] --run-id ID",
		Flags:       append(commonCLIFlags(), cliFlagSpec{Name: "run-id", Description: "run ID recorded in the stale lock", ValueName: "ID"}),
	},
	{
		Name:        "change",
		Description: "change a job in the current or previous batch",
		Usage:       "rotari change [--basedir DIR] [--project-name NAME] [--run-id ID] [--job-id ID|--job-name NAME] [--executor EXECUTOR] [--executor-option OPTION] [--clear-executor-options] [--set-job-name NAME] [--depends-on NAME] [--clear-depends-on] [-- command ...]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID to use when restoring a batch", ValueName: "ID"},
			cliFlagSpec{Name: "job-id", Description: "target job ID", ValueName: "ID"},
			cliFlagSpec{Name: "job-name", Description: "target job name", ValueName: "NAME"},
			cliFlagSpec{Name: "executor", Description: "replace job executor", ValueName: "EXECUTOR", Values: executorNames()},
			cliFlagSpec{Name: "executor-option", Description: "replace executor options", ValueName: "OPTION"},
			cliFlagSpec{Name: "clear-executor-options", Description: "clear executor options"},
			cliFlagSpec{Name: "set-job-name", Description: "replace job name", ValueName: "NAME"},
			cliFlagSpec{Name: "depends-on", Description: "replace prerequisites; may be repeated", ValueName: "NAME"},
			cliFlagSpec{Name: "clear-depends-on", Description: "clear prerequisites"},
		),
		HasPositional: true,
	},
	{
		Name:        "remove",
		Description: "remove jobs from the current or previous batch",
		Usage:       "rotari remove [--basedir DIR] [--project-name NAME] [--run-id ID] [--job-id ID]...|[--job-name NAME]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID to use when restoring a batch", ValueName: "ID"},
			cliFlagSpec{Name: "job-id", Description: "remove a job; may be repeated", ValueName: "ID"},
			cliFlagSpec{Name: "job-name", Description: "remove a job by name", ValueName: "NAME"},
		),
	},
	{
		Name:        "show",
		Description: "show queue or run status",
		Usage:       "rotari show [--basedir DIR] [--project-name NAME] [--run-id ID] [--job-id ID] [--failed] [--logs] [--failed-logs] [--follow] [--no-pager] [--runs]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID", ValueName: "ID"},
			cliFlagSpec{Name: "job-id", Description: "job ID", ValueName: "ID"},
			cliFlagSpec{Name: "failed", Description: "show failed jobs only"},
			cliFlagSpec{Name: "logs", Description: "print output logs for all jobs"},
			cliFlagSpec{Name: "failed-logs", Description: "print output logs for failed jobs"},
			cliFlagSpec{Name: "follow", Description: "follow log output until the run completes"},
			cliFlagSpec{Name: "no-pager", Description: "print logs directly instead of using a pager"},
			cliFlagSpec{Name: "runs", Description: "list all runs in the project"},
		),
	},
	{
		Name:        "wait",
		Description: "wait for an asynchronous run",
		Usage:       "rotari wait [--basedir DIR] [--project-name NAME] --run-id ID [--timeout DURATION]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID", ValueName: "ID"},
			cliFlagSpec{Name: "timeout", Description: "maximum wait duration", ValueName: "DURATION"},
		),
	},
	{
		Name:        "add",
		Description: "add a command to a queue",
		Usage:       "rotari add [--basedir DIR] [--project-name NAME] [--executor EXECUTOR] [--executor-option OPTION] [--job-name NAME] [--depends-on NAME] [--run] <command ...>",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "executor", Description: "job executor", ValueName: "EXECUTOR", Values: executorNames()},
			cliFlagSpec{Name: "executor-option", Description: "option passed to the selected scheduler (sbatch/qsub/...)", ValueName: "OPTION"},
			cliFlagSpec{Name: "job-name", Description: "job name label", ValueName: "NAME"},
			cliFlagSpec{Name: "name", Description: "job name label (alias)", ValueName: "NAME"},
			cliFlagSpec{Name: "depends-on", Description: "name of a prerequisite job; may be repeated", ValueName: "NAME"},
			cliFlagSpec{Name: "run", Description: "execute the queue after adding the command"},
		),
		HasPositional: true,
	},
	{
		Name:        "copy",
		Description: "copy jobs from a run into the queue",
		Usage:       "rotari copy [--basedir DIR] [--project-name NAME] --run-id ID [--failed] [--unfinished] [--success] [--job-id ID] [--append|--overwrite]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "source run ID", ValueName: "ID"},
			cliFlagSpec{Name: "failed", Description: "include failed jobs; may be combined with result filters"},
			cliFlagSpec{Name: "unfinished", Description: "include unfinished jobs; may be combined with result filters"},
			cliFlagSpec{Name: "success", Description: "include successful jobs; may be combined with result filters"},
			cliFlagSpec{Name: "job-id", Description: "copy a job; may be repeated", ValueName: "ID"},
			cliFlagSpec{Name: "append", Description: "append to a non-empty queue"},
			cliFlagSpec{Name: "overwrite", Description: "replace a non-empty queue"},
		),
	},
	{
		Name:        "run",
		Description: "execute queued commands, optionally selecting jobs from a run",
		Usage:       "rotari run [--basedir DIR] [--project-name NAME] [--run-id ID] [--overwrite] [--run-name NAME] [--local-concurrency N] [--batch-concurrency N] [--retry N] [--failed] [--unfinished] [--success] [--job-id ID] [--async] [--executor EXECUTOR] [--executor-option OPTION]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "repopulate the queue from this run before executing (copy --run-id + run); defaults to the latest run when a result filter is used", ValueName: "ID"},
			cliFlagSpec{Name: "overwrite", Description: "replace a non-empty queue without prompting; requires --run-id"},
			cliFlagSpec{Name: "run-name", Description: "run name label", ValueName: "NAME"},
			cliFlagSpec{Name: "local-concurrency", Description: "local worker concurrency", ValueName: "N"},
			cliFlagSpec{Name: "batch-concurrency", Description: "scheduler job concurrency (Slurm/PBS/...)", ValueName: "N"},
			cliFlagSpec{Name: "retry", Description: "retry failed jobs up to N times", ValueName: "N"},
			cliFlagSpec{Name: "failed", Description: "only execute failed jobs; others carry forward their previous result"},
			cliFlagSpec{Name: "unfinished", Description: "only execute unfinished jobs; others carry forward their previous result"},
			cliFlagSpec{Name: "success", Description: "only execute successful jobs; others carry forward their previous result"},
			cliFlagSpec{Name: "job-id", Description: "only execute this job; may be repeated; others carry forward their previous result", ValueName: "ID"},
			cliFlagSpec{Name: "async", Description: "return after starting the run"},
			cliFlagSpec{Name: "executor", Description: "execution executor override", ValueName: "EXECUTOR", Values: executorNames()},
			cliFlagSpec{Name: "executor-option", Description: "option passed to the selected scheduler (sbatch/qsub/...)", ValueName: "OPTION"},
		),
	},
	{
		Name:        "retry",
		Description: "alias for run --failed --unfinished",
		Usage:       "rotari retry [--basedir DIR] [--project-name NAME] [--run-id ID] [--overwrite] [--run-name NAME] [--local-concurrency N] [--batch-concurrency N] [--retry N] [--job-id ID] [--async] [--executor EXECUTOR] [--executor-option OPTION]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "repopulate the queue from this run before executing; defaults to the latest run", ValueName: "ID"},
			cliFlagSpec{Name: "overwrite", Description: "replace a non-empty queue without prompting; requires --run-id"},
			cliFlagSpec{Name: "run-name", Description: "run name label", ValueName: "NAME"},
			cliFlagSpec{Name: "local-concurrency", Description: "local worker concurrency", ValueName: "N"},
			cliFlagSpec{Name: "batch-concurrency", Description: "scheduler job concurrency (Slurm/PBS/...)", ValueName: "N"},
			cliFlagSpec{Name: "retry", Description: "retry failed jobs up to N times", ValueName: "N"},
			cliFlagSpec{Name: "job-id", Description: "also execute this job; may be repeated", ValueName: "ID"},
			cliFlagSpec{Name: "async", Description: "return after starting the run"},
			cliFlagSpec{Name: "executor", Description: "execution executor override", ValueName: "EXECUTOR", Values: executorNames()},
			cliFlagSpec{Name: "executor-option", Description: "option passed to the selected scheduler (sbatch/qsub/...)", ValueName: "OPTION"},
		),
	},
	{
		Name:        "server",
		Description: "manage the background server",
		Usage:       "rotari server <status|list|shutdown> [--basedir DIR] [--masterdir DIR]",
		Flags: []cliFlagSpec{
			{Name: "basedir", Description: "state directory", ValueName: "DIR"},
			{Name: "masterdir", Description: "server registry directory", ValueName: "DIR"},
		},
		Subcommands: []cliSubcommandSpec{
			{Name: "status", Description: "show server status"},
			{Name: "list", Description: "list servers"},
			{Name: "shutdown", Description: "shut down server"},
		},
	},
	{
		Name:        "web",
		Description: "serve the web status UI",
		Usage:       "rotari web [--basedir DIR] [--project-name NAME] [--host HOST] [--port PORT] [--static-dir DIR]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "host", Description: "HTTP listen host", ValueName: "HOST"},
			cliFlagSpec{Name: "port", Description: "HTTP listen port", ValueName: "PORT"},
			cliFlagSpec{Name: "static-dir", Description: "generate a static web UI", ValueName: "DIR"},
		),
	},
	{
		Name:        "completion",
		Description: "print shell completion script",
		Usage:       "rotari completion <bash|zsh|install [bash|zsh]>",
		Subcommands: []cliSubcommandSpec{
			{Name: "bash", Description: "bash completion"},
			{Name: "zsh", Description: "zsh completion"},
			{Name: "install", Description: "install completion for the current shell"},
		},
	},
	{
		Name:        "version",
		Description: "print version",
		Usage:       "rotari version",
	},
}

func cliCommandNames() []string {
	names := make([]string, 0, len(cliCommandSpecs))
	for _, command := range cliCommandSpecs {
		names = append(names, command.Name)
	}
	return names
}

func cliUsage(name string) string {
	for _, command := range cliCommandSpecs {
		if command.Name == name {
			return command.Usage
		}
	}
	return "rotari " + name
}

func cliSubcommandNames(name string) []string {
	for _, command := range cliCommandSpecs {
		if command.Name != name {
			continue
		}
		names := make([]string, 0, len(command.Subcommands))
		for _, subcommand := range command.Subcommands {
			names = append(names, subcommand.Name)
		}
		return names
	}
	return nil
}

func cliFlag(name string) cliFlagSpec {
	for _, command := range cliCommandSpecs {
		for _, flagSpec := range command.Flags {
			if flagSpec.Name == name {
				return flagSpec
			}
		}
	}
	panic("undefined CLI flag: " + name)
}

func cliString(fs *flag.FlagSet, name, defaultValue string) *string {
	spec := cliFlag(name)
	target := new(string)
	fs.StringVar(target, spec.Name, defaultValue, spec.Description)
	if short := cliShortFlagNames[name]; short != "" {
		fs.StringVar(target, short, defaultValue, spec.Description+" (shorthand)")
	}
	return target
}

func cliStringVar(fs *flag.FlagSet, target *string, name, defaultValue string) {
	spec := cliFlag(name)
	fs.StringVar(target, spec.Name, defaultValue, spec.Description)
	if short := cliShortFlagNames[name]; short != "" {
		fs.StringVar(target, short, defaultValue, spec.Description+" (shorthand)")
	}
}

func cliBool(fs *flag.FlagSet, name string, defaultValue bool) *bool {
	spec := cliFlag(name)
	target := new(bool)
	fs.BoolVar(target, spec.Name, defaultValue, spec.Description)
	if short := cliShortFlagNames[name]; short != "" {
		fs.BoolVar(target, short, defaultValue, spec.Description+" (shorthand)")
	}
	return target
}

func cliInt(fs *flag.FlagSet, name string, defaultValue int) *int {
	spec := cliFlag(name)
	target := new(int)
	fs.IntVar(target, spec.Name, defaultValue, spec.Description)
	if short := cliShortFlagNames[name]; short != "" {
		fs.IntVar(target, short, defaultValue, spec.Description+" (shorthand)")
	}
	return target
}

func cliDuration(fs *flag.FlagSet, name string, defaultValue time.Duration) *time.Duration {
	spec := cliFlag(name)
	target := new(time.Duration)
	fs.DurationVar(target, spec.Name, defaultValue, spec.Description)
	if short := cliShortFlagNames[name]; short != "" {
		fs.DurationVar(target, short, defaultValue, spec.Description+" (shorthand)")
	}
	return target
}

func cliValue(fs *flag.FlagSet, target flag.Value, name string) {
	spec := cliFlag(name)
	fs.Var(target, spec.Name, spec.Description)
	if short := cliShortFlagNames[name]; short != "" {
		fs.Var(target, short, spec.Description+" (shorthand)")
	}
}
