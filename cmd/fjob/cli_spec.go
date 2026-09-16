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

func commonCLIFlags() []cliFlagSpec {
	return []cliFlagSpec{
		{Name: "basedir", Description: "state directory", ValueName: "DIR"},
		{Name: "queue-name", Description: "queue name", ValueName: "NAME"},
	}
}

var cliCommandSpecs = []cliCommandSpec{
	{
		Name:        "check",
		Description: "check queue and dependencies",
		Usage:       "fjob check [--basedir DIR] [--queue-name NAME] [--server]",
		Flags: append(commonCLIFlags(), cliFlagSpec{
			Name: "server", Description: "also require a running server",
		}),
	},
	{
		Name:        "cancel",
		Description: "cancel the current run",
		Usage:       "fjob cancel [--basedir DIR] [--queue-name NAME] [--job-id ID] [--wait]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "job-id", Description: "cancel a running job; may be repeated", ValueName: "ID"},
			cliFlagSpec{Name: "wait", Description: "wait until cancellation is complete"},
		),
	},
	{
		Name:        "suspend",
		Description: "suspend running jobs",
		Usage:       "fjob suspend [--basedir DIR] [--queue-name NAME] [--job-id ID]...",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "job-id", Description: "suspend a running job; may be repeated", ValueName: "ID"},
		),
	},
	{
		Name:        "resume",
		Description: "resume suspended jobs",
		Usage:       "fjob resume [--basedir DIR] [--queue-name NAME] [--job-id ID]...",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "job-id", Description: "resume a suspended job; may be repeated", ValueName: "ID"},
		),
	},
	{
		Name:        "delete",
		Description: "delete saved run history",
		Usage:       "fjob delete [--basedir DIR] [--queue-name NAME] [--run-id ID]",
		Flags:       append(commonCLIFlags(), cliFlagSpec{Name: "run-id", Description: "delete only the specified run", ValueName: "ID"}),
	},
	{
		Name:        "change",
		Description: "change a job in the current or previous batch",
		Usage:       "fjob change [--basedir DIR] [--queue-name NAME] [--run-id ID] [--job-id ID|--job-name NAME] [--executor EXECUTOR] [--executor-option OPTION] [--clear-executor-options] [--set-job-name NAME] [--depends-on NAME] [--clear-depends-on] [-- command ...]",
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
		Usage:       "fjob remove [--basedir DIR] [--queue-name NAME] [--run-id ID] [--job-id ID]...|[--job-name NAME]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID to use when restoring a batch", ValueName: "ID"},
			cliFlagSpec{Name: "job-id", Description: "remove a job; may be repeated", ValueName: "ID"},
			cliFlagSpec{Name: "job-name", Description: "remove a job by name", ValueName: "NAME"},
		),
	},
	{
		Name:        "show",
		Description: "show queue or run status",
		Usage:       "fjob show [--basedir DIR] [--queue-name NAME] [--run-id ID] [--job-id ID] [--failed] [--logs] [--failed-logs] [--follow] [--no-pager] [--runs]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID", ValueName: "ID"},
			cliFlagSpec{Name: "job-id", Description: "job ID", ValueName: "ID"},
			cliFlagSpec{Name: "failed", Description: "show failed jobs only"},
			cliFlagSpec{Name: "logs", Description: "print output logs for all jobs"},
			cliFlagSpec{Name: "failed-logs", Description: "print output logs for failed jobs"},
			cliFlagSpec{Name: "follow", Description: "follow log output until the run completes"},
			cliFlagSpec{Name: "no-pager", Description: "print logs directly instead of using a pager"},
			cliFlagSpec{Name: "runs", Description: "list all runs in the queue"},
		),
	},
	{
		Name:        "wait",
		Description: "wait for an asynchronous run",
		Usage:       "fjob wait [--basedir DIR] [--queue-name NAME] --run-id ID [--timeout DURATION]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID", ValueName: "ID"},
			cliFlagSpec{Name: "timeout", Description: "maximum wait duration", ValueName: "DURATION"},
		),
	},
	{
		Name:        "add",
		Description: "add a command to a queue",
		Usage:       "fjob add [--basedir DIR] [--queue-name NAME] [--executor EXECUTOR] [--executor-option OPTION] [--job-name NAME] [--depends-on NAME] <command ...>",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "executor", Description: "job executor", ValueName: "EXECUTOR", Values: executorNames()},
			cliFlagSpec{Name: "executor-option", Description: "option passed to the selected scheduler (sbatch/qsub/...)", ValueName: "OPTION"},
			cliFlagSpec{Name: "job-name", Description: "job name label", ValueName: "NAME"},
			cliFlagSpec{Name: "name", Description: "job name label (alias)", ValueName: "NAME"},
			cliFlagSpec{Name: "depends-on", Description: "name of a prerequisite job; may be repeated", ValueName: "NAME"},
		),
		HasPositional: true,
	},
	{
		Name:        "copy",
		Description: "copy jobs from a run into the queue",
		Usage:       "fjob copy [--basedir DIR] [--queue-name NAME] --run-id ID [--failed|--unfinished|--success|--nonsuccess|--job-id ID] [--append|--overwrite]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "source run ID", ValueName: "ID"},
			cliFlagSpec{Name: "failed", Description: "copy failed jobs only"},
			cliFlagSpec{Name: "unfinished", Description: "copy unfinished jobs only"},
			cliFlagSpec{Name: "success", Description: "copy successful jobs only"},
			cliFlagSpec{Name: "nonsuccess", Description: "copy failed and unfinished jobs only"},
			cliFlagSpec{Name: "job-id", Description: "copy a job; may be repeated", ValueName: "ID"},
			cliFlagSpec{Name: "append", Description: "append to a non-empty queue"},
			cliFlagSpec{Name: "overwrite", Description: "replace a non-empty queue"},
		),
	},
	{
		Name:        "run",
		Description: "execute queued commands",
		Usage:       "fjob run [--basedir DIR] [--queue-name NAME] [--run-name NAME] [--local-concurrency N] [--batch-concurrency N] [--retry N] [--async] [--executor EXECUTOR] [--executor-option OPTION]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-name", Description: "run name label", ValueName: "NAME"},
			cliFlagSpec{Name: "local-concurrency", Description: "local worker concurrency", ValueName: "N"},
			cliFlagSpec{Name: "batch-concurrency", Description: "scheduler job concurrency (Slurm/PBS/...)", ValueName: "N"},
			cliFlagSpec{Name: "retry", Description: "retry failed jobs up to N times", ValueName: "N"},
			cliFlagSpec{Name: "async", Description: "return after starting the run"},
			cliFlagSpec{Name: "executor", Description: "execution executor override", ValueName: "EXECUTOR", Values: executorNames()},
			cliFlagSpec{Name: "executor-option", Description: "option passed to the selected scheduler (sbatch/qsub/...)", ValueName: "OPTION"},
		),
	},
	{
		Name:        "rerun",
		Description: "rerun selected jobs from the latest run",
		Usage:       "fjob rerun [--basedir DIR] [--queue-name NAME] [--run-name NAME] [--local-concurrency N] [--batch-concurrency N] [--retry N] [--failed|--unfinished|--success|--nonsuccess|--job-id ID] [--async] [--executor EXECUTOR] [--executor-option OPTION]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-name", Description: "run name label", ValueName: "NAME"},
			cliFlagSpec{Name: "local-concurrency", Description: "local worker concurrency", ValueName: "N"},
			cliFlagSpec{Name: "batch-concurrency", Description: "scheduler job concurrency (Slurm/PBS/...)", ValueName: "N"},
			cliFlagSpec{Name: "retry", Description: "retry failed jobs up to N times", ValueName: "N"},
			cliFlagSpec{Name: "failed", Description: "rerun failed jobs from the latest run"},
			cliFlagSpec{Name: "unfinished", Description: "rerun unfinished jobs from the latest run"},
			cliFlagSpec{Name: "success", Description: "rerun successful jobs from the latest run"},
			cliFlagSpec{Name: "nonsuccess", Description: "rerun failed and unfinished jobs from the latest run"},
			cliFlagSpec{Name: "job-id", Description: "rerun a job from the latest run; may be repeated", ValueName: "ID"},
			cliFlagSpec{Name: "async", Description: "return after starting the run"},
			cliFlagSpec{Name: "executor", Description: "execution executor override", ValueName: "EXECUTOR", Values: executorNames()},
			cliFlagSpec{Name: "executor-option", Description: "option passed to the selected scheduler (sbatch/qsub/...)", ValueName: "OPTION"},
		),
	},
	{
		Name:        "server",
		Description: "manage the background server",
		Usage:       "fjob server <status|list|shutdown> [--basedir DIR] [--masterdir DIR]",
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
		Usage:       "fjob web [--basedir DIR] [--queue-name NAME] [--host HOST] [--port PORT]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "host", Description: "HTTP listen host", ValueName: "HOST"},
			cliFlagSpec{Name: "port", Description: "HTTP listen port", ValueName: "PORT"},
		),
	},
	{
		Name:        "completion",
		Description: "print shell completion script",
		Usage:       "fjob completion <bash|zsh|install [bash|zsh]>",
		Subcommands: []cliSubcommandSpec{
			{Name: "bash", Description: "bash completion"},
			{Name: "zsh", Description: "zsh completion"},
			{Name: "install", Description: "install completion for the current shell"},
		},
	},
	{
		Name:        "version",
		Description: "print version",
		Usage:       "fjob version",
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
	return "fjob " + name
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
	return fs.String(spec.Name, defaultValue, spec.Description)
}

func cliStringVar(fs *flag.FlagSet, target *string, name, defaultValue string) {
	spec := cliFlag(name)
	fs.StringVar(target, spec.Name, defaultValue, spec.Description)
}

func cliBool(fs *flag.FlagSet, name string, defaultValue bool) *bool {
	spec := cliFlag(name)
	return fs.Bool(spec.Name, defaultValue, spec.Description)
}

func cliInt(fs *flag.FlagSet, name string, defaultValue int) *int {
	spec := cliFlag(name)
	return fs.Int(spec.Name, defaultValue, spec.Description)
}

func cliDuration(fs *flag.FlagSet, name string, defaultValue time.Duration) *time.Duration {
	spec := cliFlag(name)
	return fs.Duration(spec.Name, defaultValue, spec.Description)
}

func cliValue(fs *flag.FlagSet, target flag.Value, name string) {
	spec := cliFlag(name)
	fs.Var(target, spec.Name, spec.Description)
}
