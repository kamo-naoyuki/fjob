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
		Usage:       "jobq check [--basedir DIR] [--queue-name NAME] [--server]",
		Flags: append(commonCLIFlags(), cliFlagSpec{
			Name: "server", Description: "also require a running server",
		}),
	},
	{
		Name:        "cancel",
		Description: "cancel the current run",
		Usage:       "jobq cancel [--basedir DIR] [--queue-name NAME] [--wait]",
		Flags: append(commonCLIFlags(), cliFlagSpec{
			Name: "wait", Description: "wait until cancellation is complete",
		}),
	},
	{
		Name:        "clear",
		Description: "clear saved run history",
		Usage:       "jobq clear [--basedir DIR] [--queue-name NAME]",
		Flags:       commonCLIFlags(),
	},
	{
		Name:        "show",
		Description: "show queue or run status",
		Usage:       "jobq show [--basedir DIR] [--queue-name NAME] [--run-id ID] [--job-id ID] [--failed] [--logs] [--failed-logs] [--runs]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID", ValueName: "ID"},
			cliFlagSpec{Name: "job-id", Description: "job ID", ValueName: "ID"},
			cliFlagSpec{Name: "failed", Description: "show failed jobs only"},
			cliFlagSpec{Name: "logs", Description: "print output logs for all jobs"},
			cliFlagSpec{Name: "failed-logs", Description: "print output logs for failed jobs"},
			cliFlagSpec{Name: "runs", Description: "list all runs in the queue"},
		),
	},
	{
		Name:        "wait",
		Description: "wait for an asynchronous run",
		Usage:       "jobq wait [--basedir DIR] [--queue-name NAME] --run-id ID [--timeout DURATION]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "run-id", Description: "run ID", ValueName: "ID"},
			cliFlagSpec{Name: "timeout", Description: "maximum wait duration", ValueName: "DURATION"},
		),
	},
	{
		Name:        "submit",
		Description: "add a command to a queue",
		Usage:       "jobq submit [--basedir DIR] [--queue-name NAME] [--backend BACKEND] [--sbatch-option OPTION] [--job-name NAME] <command ...>",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "backend", Description: "job backend", ValueName: "BACKEND", Values: []string{"local", "slurm"}},
			cliFlagSpec{Name: "sbatch-option", Description: "option passed to sbatch", ValueName: "OPTION"},
			cliFlagSpec{Name: "job-name", Description: "job name label", ValueName: "NAME"},
			cliFlagSpec{Name: "name", Description: "job name label (alias)", ValueName: "NAME"},
		),
		HasPositional: true,
	},
	{
		Name:        "run",
		Description: "execute queued commands",
		Usage:       "jobq run [--basedir DIR] [--queue-name NAME] [--local-concurrency N] [--slurm-max-active N] [--retry N] [--failed|--unfinished|--success|--nonsuccess] [--async] [--backend BACKEND] [--sbatch-option OPTION]",
		Flags: append(commonCLIFlags(),
			cliFlagSpec{Name: "local-concurrency", Description: "local worker concurrency", ValueName: "N"},
			cliFlagSpec{Name: "slurm-max-active", Description: "maximum active Slurm jobs", ValueName: "N"},
			cliFlagSpec{Name: "retry", Description: "retry failed jobs up to N times", ValueName: "N"},
			cliFlagSpec{Name: "failed", Description: "run failed jobs from the latest run"},
			cliFlagSpec{Name: "unfinished", Description: "run unfinished jobs from the latest run"},
			cliFlagSpec{Name: "success", Description: "run successful jobs from the latest run"},
			cliFlagSpec{Name: "nonsuccess", Description: "run failed and unfinished jobs from the latest run"},
			cliFlagSpec{Name: "async", Description: "return after starting the run"},
			cliFlagSpec{Name: "backend", Description: "execution backend override", ValueName: "BACKEND", Values: []string{"local", "slurm"}},
			cliFlagSpec{Name: "sbatch-option", Description: "option passed to sbatch", ValueName: "OPTION"},
		),
	},
	{
		Name:        "server",
		Description: "manage the background server",
		Usage:       "jobq server <status|list|shutdown> [--basedir DIR] [--masterdir DIR]",
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
		Name:        "completion",
		Description: "print shell completion script",
		Usage:       "jobq completion <bash|zsh|install [bash|zsh]>",
		Subcommands: []cliSubcommandSpec{
			{Name: "bash", Description: "bash completion"},
			{Name: "zsh", Description: "zsh completion"},
			{Name: "install", Description: "install completion for the current shell"},
		},
	},
	{
		Name:        "version",
		Description: "print version",
		Usage:       "jobq version",
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
	return "jobq " + name
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
