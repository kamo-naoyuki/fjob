package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdCompletion(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jobq completion <bash|zsh|install [bash|zsh]>")
		return 1
	}
	if args[0] == "install" {
		if len(args) > 2 {
			fmt.Fprintln(os.Stderr, "usage: jobq completion install [bash|zsh]")
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
		return appendCompletionBlock(path, "bash", `if command -v jobq >/dev/null 2>&1; then
    eval "$(jobq completion bash)"
fi`)
	case "zsh":
		zfuncDir := filepath.Join(home, ".zfunc")
		if err := os.MkdirAll(zfuncDir, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", zfuncDir, err)
		}
		completionPath := filepath.Join(zfuncDir, "_jobq")
		if err := os.WriteFile(completionPath, []byte(generateZshCompletion()), 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", completionPath, err)
		}
		rcPath := filepath.Join(home, ".zshrc")
		if err := appendCompletionBlock(rcPath, "zsh", `fpath=("$HOME/.zfunc" $fpath)
autoload -Uz compinit && compinit`); err != nil {
			return err
		}
		fmt.Printf("installed zsh completion: %s\n", completionPath)
		return nil
	default:
		return fmt.Errorf("unsupported shell %q; specify bash or zsh", shell)
	}
}

func appendCompletionBlock(path, shell, block string) error {
	marker := "# jobq completion (" + shell + ")"
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
	builder.WriteString("# bash completion for jobq\n_jobq_completion() {\n")
	builder.WriteString("    local cur prev command\n    cur=\"${COMP_WORDS[COMP_CWORD]}\"\n")
	builder.WriteString("    prev=\"${COMP_WORDS[COMP_CWORD-1]}\"\n    command=\"${COMP_WORDS[1]}\"\n\n")
	builder.WriteString("    if [[ ${COMP_CWORD} -eq 1 ]]; then\n")
	fmt.Fprintf(&builder, "        COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", strings.Join(cliCommandNames(), " "))
	builder.WriteString("        return\n    fi\n\n    case \"$prev\" in\n")
	for _, command := range cliCommandSpecs {
		for _, option := range command.Flags {
			if len(option.Values) == 0 {
				continue
			}
			fmt.Fprintf(&builder, "        --%s)\n            COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n            return\n            ;;\n", option.Name, strings.Join(option.Values, " "))
		}
	}
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
			}
			builder.WriteString("            fi\n")
		} else {
			fmt.Fprintf(&builder, "            COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n", bashOptions(command.Flags))
		}
		builder.WriteString("            ;;\n")
	}
	builder.WriteString("    esac\n}\ncomplete -F _jobq_completion jobq\n")
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
	builder.WriteString("#compdef jobq\n\n_jobq() {\n    local -a commands\n    commands=(\n")
	for _, command := range cliCommandSpecs {
		fmt.Fprintf(&builder, "        '%s:%s'\n", command.Name, command.Description)
	}
	builder.WriteString("    )\n\n    if (( CURRENT == 2 )); then\n        _describe 'command' commands\n        return\n    fi\n\n    case $words[2] in\n")
	for _, command := range cliCommandSpecs {
		if len(command.Flags) == 0 && len(command.Subcommands) == 0 {
			continue
		}
		fmt.Fprintf(&builder, "        %s)\n", command.Name)
		if len(command.Subcommands) > 0 {
			values := make([]string, 0, len(command.Subcommands))
			for _, subcommand := range command.Subcommands {
				values = append(values, fmt.Sprintf("'%s:%s'", subcommand.Name, subcommand.Description))
			}
			fmt.Fprintf(&builder, "            if (( CURRENT == 3 )); then\n                _describe 'subcommand' %s\n            else\n", strings.Join(values, " "))
			if len(command.Flags) > 0 {
				fmt.Fprintf(&builder, "                _arguments %s\n", zshArguments(command.Flags))
			}
			builder.WriteString("            fi\n")
		} else {
			if len(command.Flags) > 0 {
				fmt.Fprintf(&builder, "            if [[ $words[CURRENT] == -* ]]; then\n                compadd -- %s\n                return\n            fi\n", zshOptionNames(command.Flags))
			}
			arguments := zshArguments(command.Flags)
			if command.HasPositional {
				arguments += " '*:command:_command_names'"
			}
			fmt.Fprintf(&builder, "            _arguments %s\n", arguments)
		}
		builder.WriteString("            ;;\n")
	}
	builder.WriteString("    esac\n}\n")
	return builder.String()
}

func zshArguments(flags []cliFlagSpec) string {
	arguments := make([]string, 0, len(flags))
	for _, flag := range flags {
		argument := fmt.Sprintf("'--%s[%s]", flag.Name, flag.Description)
		if len(flag.Values) > 0 {
			argument += ":" + flag.ValueName + ":(" + strings.Join(flag.Values, " ") + ")"
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
