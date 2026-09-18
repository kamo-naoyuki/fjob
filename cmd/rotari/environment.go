package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	envBaseDir      = "ROTARI_BASEDIR"
	envProjectName  = "ROTARI_PROJECT_NAME"
	envMasterDir    = "ROTARI_MASTERDIR"
	envRunID        = "ROTARI_RUN_ID"
	envJobID        = "ROTARI_JOB_ID"
	envJobName      = "ROTARI_JOB_NAME"
	envExecutor     = "ROTARI_EXECUTOR"
	envExecutorOpts = "ROTARI_EXECUTOR_OPTIONS"
	envRunName      = "ROTARI_RUN_NAME"
	envRunLocalConc = "ROTARI_RUN_LOCAL_CONCURRENCY"
	envRunBatchConc = "ROTARI_RUN_BATCH_CONCURRENCY"
	envRunRetry     = "ROTARI_RUN_RETRY"
	envRunAsync     = "ROTARI_RUN_ASYNC"
	envArrayRange   = "ROTARI_ARRAY_RANGE"
	envCheckServer  = "ROTARI_CHECK_SERVER"
	envCheckRecover = "ROTARI_CHECK_RECOVER"
	envWaitTimeout  = "ROTARI_WAIT_TIMEOUT"
	envWebHost      = "ROTARI_WEB_HOST"
	envWebPort      = "ROTARI_WEB_PORT"
	envWebStaticDir = "ROTARI_WEB_STATIC_DIR"
	envBin          = "ROTARI_BIN"
	envRunDir       = "ROTARI_RUN_DIR"
	envJobDir       = "ROTARI_JOB_DIR"
	envCWD          = "ROTARI_CWD"
	envArrayTaskID  = "ROTARI_ARRAY_TASK_ID"
	envArrayFirst   = "ROTARI_ARRAY_FIRST"
	envArrayLast    = "ROTARI_ARRAY_LAST"
	envArraySize    = "ROTARI_ARRAY_SIZE"
)

var propagatedEnvironmentVariables = []string{
	envBaseDir, envProjectName, envMasterDir, envRunID, envJobID, envJobName,
	envExecutor, envExecutorOpts, envRunName, envRunLocalConc, envRunBatchConc,
	envRunRetry, envRunAsync, envArrayRange,
}

func validateEnvironment(environment []string) error {
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsAny(name, " \t\n=") {
			return errors.New("expected KEY=VALUE")
		}
	}
	return nil
}

type environmentDefinition struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	CLIDefault  bool   `json:"cli_default"`
	Job         bool   `json:"job"`
	Array       bool   `json:"array"`
	Description string `json:"description"`
}

func environmentDefinitions() []environmentDefinition {
	return []environmentDefinition{
		{Name: envBaseDir, CLIDefault: true, Job: true, Array: true, Description: "State directory; --basedir default."},
		{Name: envProjectName, CLIDefault: true, Job: true, Array: true, Description: "Project name; --project-name default."},
		{Name: envMasterDir, CLIDefault: true, Description: "Server registry directory; --masterdir default."},
		{Name: envRunID, CLIDefault: true, Job: true, Array: true, Description: "Current run ID; --run-id default."},
		{Name: envJobID, CLIDefault: true, Job: true, Array: true, Description: "Current job ID; --job-id default."},
		{Name: envJobName, CLIDefault: true, Job: true, Array: true, Description: "Current job name; --job-name default."},
		{Name: envExecutor, CLIDefault: true, Job: true, Array: true, Description: "Current executor; --executor default."},
		{Name: envExecutorOpts, CLIDefault: true, Job: true, Array: true, Description: "Default scheduler executor options."},
		{Name: envRunName, CLIDefault: true, Job: true, Array: true, Description: "Run name; --run-name default."},
		{Name: envRunLocalConc, CLIDefault: true, Job: true, Array: true, Description: "Local worker limit; --local-concurrency default."},
		{Name: envRunBatchConc, CLIDefault: true, Job: true, Array: true, Description: "Scheduler submission limit; --batch-concurrency default."},
		{Name: envRunRetry, CLIDefault: true, Job: true, Array: true, Description: "Retry count; --retry default."},
		{Name: envRunAsync, CLIDefault: true, Job: true, Array: true, Description: "Async run mode; --async default."},
		{Name: envArrayRange, CLIDefault: true, Job: true, Array: true, Description: "Array range; --array default."},
		{Name: envBin, Job: true, Array: true, Description: "Absolute path to the rotari binary."},
		{Name: envRunDir, Job: true, Array: true, Description: "Current run directory."},
		{Name: envJobDir, Job: true, Array: true, Description: "Current job directory."},
		{Name: envCWD, Job: true, Array: true, Description: "Working directory from which the run started."},
		{Name: envArrayTaskID, Array: true, Description: "Current array task number."},
		{Name: envArrayFirst, Array: true, Description: "First array task number."},
		{Name: envArrayLast, Array: true, Description: "Last array task number."},
		{Name: envArraySize, Array: true, Description: "Number of tasks in the array."},
		{Name: envCheckServer, CLIDefault: true, Description: "--server default for check."},
		{Name: envCheckRecover, CLIDefault: true, Description: "--recover default for check."},
		{Name: envWaitTimeout, CLIDefault: true, Description: "--timeout default for wait."},
		{Name: envWebHost, CLIDefault: true, Description: "--host default for web."},
		{Name: envWebPort, CLIDefault: true, Description: "--port default for web."},
		{Name: envWebStaticDir, CLIDefault: true, Description: "--static-dir default for web."},
	}
}

func cmdEnvironment(args []string) int {
	if len(args) != 0 {
		printError("usage: " + cliUsage("env"))
		return 1
	}
	fmt.Println("VARIABLE\tVALUE\tCLI\tJOB\tARRAY\tDESCRIPTION")
	for _, definition := range environmentDefinitions() {
		value := "-"
		if current, ok := os.LookupEnv(definition.Name); ok {
			value = current
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", definition.Name, value, yesNo(definition.CLIDefault), yesNo(definition.Job), yesNo(definition.Array), definition.Description)
	}
	return 0
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "-"
}
