package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func cmdCompletion(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: fjob completion <bash|zsh|install [bash|zsh]>")
		return 1
	}
	if args[0] == "install" {
		if len(args) > 2 {
			fmt.Fprintln(os.Stderr, "usage: fjob completion install [bash|zsh]")
			return 1
		}
		shell := ""
		if len(args) == 2 {
			shell = args[1]
		}
		if err := installCompletion(shell); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("completion"))
		return 1
	}
	switch args[0] {
	case "bash":
		fmt.Print(generateBashCompletion())
	case "zsh":
		fmt.Print(generateZshCompletion())
	default:
		fmt.Fprintf(os.Stderr, "unsupported shell: %s\n", args[0])
		return 1
	}
	return 0
}

func cmdComplete(args []string) int {
	if len(args) == 0 || (args[0] != "queue-name" && args[0] != "run-id" && args[0] != "job-id") {
		return 1
	}
	basedir, queueName := "", ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--basedir":
			if i+1 >= len(args) {
				return 1
			}
			basedir = args[i+1]
			i++
		case "--queue-name":
			if i+1 >= len(args) {
				return 1
			}
			queueName = args[i+1]
			i++
		}
	}
	baseDir, _, err := resolveBaseDir(basedir)
	if err != nil {
		return 1
	}
	if args[0] == "queue-name" {
		entries, err := os.ReadDir(filepath.Join(baseDir, "queues"))
		if err != nil {
			return 0
		}
		values := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				values = append(values, entry.Name())
			}
		}
		if len(values) > 0 {
			fmt.Println(strings.Join(values, "\n"))
		}
		return 0
	}
	queueName, err = resolveQueueName(baseDir, queueName)
	if err != nil {
		return 1
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return 1
	}

	values := make(map[string]struct{})
	if args[0] == "run-id" {
		entries, err := os.ReadDir(paths.runsDir)
		if err != nil {
			return 0
		}
		for _, entry := range entries {
			if entry.IsDir() {
				values[entry.Name()] = struct{}{}
			}
		}
	} else {
		queue, err := loadQueue(paths.queueFile)
		if err == nil {
			for _, command := range queue.Commands {
				if command.ID != "" {
					values[command.ID] = struct{}{}
				}
			}
		}
		entries, err := os.ReadDir(paths.runsDir)
		if err == nil {
			for _, run := range entries {
				if !run.IsDir() {
					continue
				}
				jobs, err := os.ReadDir(filepath.Join(paths.runsDir, run.Name()))
				if err != nil {
					continue
				}
				for _, job := range jobs {
					if job.IsDir() {
						values[job.Name()] = struct{}{}
					}
				}
			}
		}
	}

	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) > 0 {
		fmt.Println(strings.Join(result, "\n"))
	}
	return 0
}

func installCompletion(shell string) error {
	if shell == "" {
		shell = filepath.Base(os.Getenv("SHELL"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to find home directory: %w", err)
	}
	switch shell {
	case "bash":
		path := filepath.Join(home, ".bashrc")
		return appendCompletionBlock(path, "bash", `if command -v fjob >/dev/null 2>&1; then
    eval "$(fjob completion bash)"
fi`)
	case "zsh":
		zfuncDir := filepath.Join(home, ".zfunc")
		if err := os.MkdirAll(zfuncDir, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", zfuncDir, err)
		}
		completionPath := filepath.Join(zfuncDir, "_fjob")
		newCompletion := generateZshCompletion()
		completionChanged := true
		if existing, err := os.ReadFile(completionPath); err == nil && string(existing) == newCompletion {
			completionChanged = false
		} else {
			if err := os.WriteFile(completionPath, []byte(newCompletion), 0o644); err != nil {
				return fmt.Errorf("failed to write %s: %w", completionPath, err)
			}
		}
		rcPath := filepath.Join(home, ".zshrc")
		if err := appendCompletionBlock(rcPath, "zsh", `fpath=("$HOME/.zfunc" $fpath)
autoload -Uz compinit && compinit
autoload -Uz _fjob && compdef _fjob fjob`); err != nil {
			return err
		}
		if err := ensureZshCompletionRegistration(rcPath); err != nil {
			return err
		}
		if completionChanged {
			fmt.Printf("installed zsh completion: %s\n", completionPath)
		} else {
			fmt.Printf("zsh completion already installed: %s\n", completionPath)
		}
		return nil
	default:
		return fmt.Errorf("unsupported shell %q; specify bash or zsh", shell)
	}
}

func ensureZshCompletionRegistration(path string) error {
	const registration = "autoload -Uz _fjob && compdef _fjob fjob"
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}
	if strings.Contains(string(data), registration) {
		return nil
	}
	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += registration + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

func appendCompletionBlock(path, shell, block string) error {
	marker := "# fjob completion (" + shell + ")"
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}
	if strings.Contains(string(data), marker) {
		fmt.Printf("completion already installed in %s\n", path)
		return nil
	}
	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + marker + "\n" + block + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	fmt.Printf("installed %s completion in %s\n", shell, path)
	return nil
}

func generateBashCompletion() string {
	var builder strings.Builder
	builder.WriteString("# bash completion for fjob\n_fjob_completion() {\n")
	builder.WriteString("    local cur prev command\n    cur=\"${COMP_WORDS[COMP_CWORD]}\"\n")
	builder.WriteString("    prev=\"${COMP_WORDS[COMP_CWORD-1]}\"\n    command=\"${COMP_WORDS[1]}\"\n\n")
	builder.WriteString("    if [[ ${COMP_CWORD} -eq 1 ]]; then\n")
	fmt.Fprintf(&builder, "        COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", strings.Join(cliCommandNames(), " "))
	builder.WriteString("        return\n    fi\n\n    case \"$prev\" in\n")
	builder.WriteString("        --queue-name)\n            local -a __fjob_completion_args=()\n            local __i\n            for (( __i = 2; __i < ${#COMP_WORDS[@]}; __i++ )); do\n                case \"${COMP_WORDS[__i]}\" in\n                    --basedir)\n                        if (( __i + 1 < ${#COMP_WORDS[@]} )); then\n                            __fjob_completion_args+=(\"${COMP_WORDS[__i]}\" \"${COMP_WORDS[__i+1]}\")\n                            (( __i++ ))\n                        fi\n                        ;;\n                esac\n            done\n            COMPREPLY=($(compgen -W \"$(fjob __complete queue-name \"${__fjob_completion_args[@]}\" 2>/dev/null)\" -- \"$cur\"))\n            return\n            ;;\n")
	for _, command := range cliCommandSpecs {
		for _, option := range command.Flags {
			if len(option.Values) == 0 {
				continue
			}
			fmt.Fprintf(&builder, "        --%s)\n            COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n            return\n            ;;\n", option.Name, strings.Join(option.Values, " "))
		}
	}
	builder.WriteString("        --run-id)\n            local -a __fjob_completion_args=()\n            local __i\n            for (( __i = 2; __i < ${#COMP_WORDS[@]}; __i++ )); do\n                case \"${COMP_WORDS[__i]}\" in\n                    --basedir|--queue-name)\n                        if (( __i + 1 < ${#COMP_WORDS[@]} )); then\n                            __fjob_completion_args+=(\"${COMP_WORDS[__i]}\" \"${COMP_WORDS[__i+1]}\")\n                            (( __i++ ))\n                        fi\n                        ;;\n                esac\n            done\n            COMPREPLY=($(compgen -W \"$(fjob __complete run-id \"${__fjob_completion_args[@]}\" 2>/dev/null)\" -- \"$cur\"))\n            return\n            ;;\n        --job-id)\n            local -a __fjob_completion_args=()\n            local __i\n            for (( __i = 2; __i < ${#COMP_WORDS[@]}; __i++ )); do\n                case \"${COMP_WORDS[__i]}\" in\n                    --basedir|--queue-name)\n                        if (( __i + 1 < ${#COMP_WORDS[@]} )); then\n                            __fjob_completion_args+=(\"${COMP_WORDS[__i]}\" \"${COMP_WORDS[__i+1]}\")\n                            (( __i++ ))\n                        fi\n                        ;;\n                esac\n            done\n            COMPREPLY=($(compgen -W \"$(fjob __complete job-id \"${__fjob_completion_args[@]}\" 2>/dev/null)\" -- \"$cur\"))\n            return\n            ;;\n")
	completionValues := cliSubcommandNames("completion")
	fmt.Fprintf(&builder, "        completion)\n            COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n            return\n            ;;\n    esac\n\n    case \"$command\" in\n", strings.Join(completionValues, " "))
	for _, command := range cliCommandSpecs {
		if len(command.Flags) == 0 && len(command.Subcommands) == 0 {
			continue
		}
		fmt.Fprintf(&builder, "        %s)\n", command.Name)
		if len(command.Subcommands) > 0 {
			values := make([]string, 0, len(command.Subcommands))
			for _, subcommand := range command.Subcommands {
				values = append(values, subcommand.Name)
			}
			fmt.Fprintf(&builder, "            if [[ ${COMP_CWORD} -eq 2 ]]; then\n                COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n            else\n", strings.Join(values, " "))
			if len(command.Flags) > 0 {
				fmt.Fprintf(&builder, "                COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", bashOptions(command.Flags))
			} else {
				builder.WriteString("                COMPREPLY=()\n")
			}
			builder.WriteString("            fi\n")
		} else {
			fmt.Fprintf(&builder, "            COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", bashOptions(command.Flags))
		}
		builder.WriteString("            ;;\n")
	}
	builder.WriteString("    esac\n}\ncomplete -F _fjob_completion fjob\n")
	return builder.String()
}

func bashOptions(flags []cliFlagSpec) string {
	options := make([]string, 0, len(flags))
	for _, flag := range flags {
		options = append(options, "--"+flag.Name)
	}
	return strings.Join(options, " ")
}

func generateZshCompletion() string {
	var builder strings.Builder
	builder.WriteString("#compdef fjob\n\n_fjob_run_ids() {\n    local -a args\n    args=(\"${words[@]:3}\")\n    reply=(\"${(@f)$(fjob __complete run-id \"${args[@]}\" 2>/dev/null)}\")\n}\n_fjob_job_ids() {\n    local -a args\n    args=(\"${words[@]:3}\")\n    reply=(\"${(@f)$(fjob __complete job-id \"${args[@]}\" 2>/dev/null)}\")\n}\n\n_fjob() {\n    local -a commands\n    commands=(\n")
	for _, command := range cliCommandSpecs {
		fmt.Fprintf(&builder, "        '%s:%s'\n", command.Name, command.Description)
	}
	builder.WriteString("    )\n\n    if (( CURRENT == 2 )); then\n        _describe 'command' commands\n        return\n    fi\n\n    case $words[2] in\n")
	subcommandIndex := 0
	for _, command := range cliCommandSpecs {
		if len(command.Flags) == 0 && len(command.Subcommands) == 0 {
			continue
		}
		fmt.Fprintf(&builder, "        %s)\n", command.Name)
		if len(command.Subcommands) > 0 {
			arrayName := fmt.Sprintf("subcommands%d", subcommandIndex)
			subcommandIndex++
			fmt.Fprintf(&builder, "            local -a %s\n            %s=(\n", arrayName, arrayName)
			for _, subcommand := range command.Subcommands {
				fmt.Fprintf(&builder, "                '%s:%s'\n", subcommand.Name, subcommand.Description)
			}
			fmt.Fprintf(&builder, "            )\n            if (( CURRENT == 3 )); then\n                _describe 'subcommand' %s\n            else\n", arrayName)
			if len(command.Flags) > 0 {
				fmt.Fprintf(&builder, "                _arguments %s\n", zshArguments(command.Flags))
			}
			builder.WriteString("            fi\n")
		} else {
			if len(command.Flags) > 0 {
				fmt.Fprintf(&builder, "            case $words[CURRENT-1] in\n                --run-id)\n                    _fjob_run_ids\n                    compadd -- $reply\n                    return\n                    ;;\n                --job-id)\n                    _fjob_job_ids\n                    compadd -- $reply\n                    return\n                    ;;\n            esac\n            if [[ $words[CURRENT] == -* ]]; then\n                compadd -- %s\n                return\n            fi\n", zshOptionNames(command.Flags))
			}
			arguments := zshArguments(command.Flags)
			if command.HasPositional {
				arguments += " '*:command:_command_names'"
			}
			fmt.Fprintf(&builder, "            _arguments %s\n", arguments)
		}
		builder.WriteString("            ;;\n")
	}
	builder.WriteString("    esac\n}\n\n_fjob_completion_context() {\n    local -a args\n    local i\n    for (( i = 3; i <= ${#words[@]}; i++ )); do\n        case $words[i] in\n            --basedir|--queue-name)\n                if (( i + 1 <= ${#words[@]} )); then\n                    args+=(\"$words[i]\" \"$words[i+1]\")\n                    (( i++ ))\n                fi\n                ;;\n        esac\n    done\n    reply=(\"${(@f)$(fjob __complete \"$1\" \"${args[@]}\" 2>/dev/null)}\")\n}\n_fjob_run_ids() { _fjob_completion_context run-id }\n_fjob_job_ids() { _fjob_completion_context job-id }\n\nif (( $+functions[compdef] )); then\n    compdef _fjob fjob\nfi\n")
	builder.WriteString("_fjob_queue_names() { _fjob_completion_context queue-name }\n")
	return builder.String()
}

func zshArguments(flags []cliFlagSpec) string {
	arguments := make([]string, 0, len(flags))
	for _, flag := range flags {
		argument := fmt.Sprintf("'--%s[%s]", flag.Name, flag.Description)
		if len(flag.Values) > 0 {
			argument += ":" + flag.ValueName + ":(" + strings.Join(flag.Values, " ") + ")"
		} else if flag.Name == "queue-name" || flag.Name == "run-id" || flag.Name == "job-id" {
			action := "_fjob_" + strings.ReplaceAll(flag.Name, "-", "_") + "s"
			argument += ":" + flag.ValueName + ":" + action
		} else if flag.ValueName != "" {
			argument += ":" + flag.ValueName + ":"
		}
		arguments = append(arguments, argument+"'")
	}
	return strings.Join(arguments, " ")
}

func zshOptionNames(flags []cliFlagSpec) string {
	options := make([]string, 0, len(flags))
	for _, flag := range flags {
		options = append(options, "--"+flag.Name)
	}
	return strings.Join(options, " ")
}
