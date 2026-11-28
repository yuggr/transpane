package cli

import (
	"fmt"
	"strings"
)

type parsedArgs struct {
	source         string
	cwd            string
	session        string
	command        string
	inspectSession string
	help           bool
	version        bool
}

const helpText = `usage: transpane [-h] [--version] [--source {all,codex,claude}] [--cwd PATH | --session ID]
                 {doctor,sessions,inspect} ...

Codex / Claude Code のセッションログを日本語表示する独立 TUI

positional arguments:
  {doctor,sessions,inspect}
    doctor              設定・読取・キャッシュ・翻訳サーバーを診断する
    sessions            セッション一覧を表示する（本文なし）
    inspect             セッションの形式とイベント件数を表示する（本文なし）

options:
  -h, --help            show this help message and exit
  --version             バージョンとコミットを表示する
  --source {all,codex,claude}
                        対象の取得元: all（既定）/ codex / claude。subcommand より前に指定する
  --cwd PATH            完全一致で絞り込む cwd（Linux 側の絶対パス）
  --session ID          明示的に選択するセッションID（codex:<id> / claude:<id> も可）
`

func parseArgs(args []string) (parsedArgs, error) {
	parsed := parsedArgs{source: "all"}
	index := 0
	for index < len(args) {
		arg := args[index]
		expanded, err := canonicalOption(arg)
		if err != nil {
			return parsed, err
		}
		arg = expanded
		if parsed.command == "" {
			switch {
			case arg == "-h" || arg == "--help":
				parsed.help = true
				index++
			case arg == "--version":
				parsed.version = true
				index++
			case arg == "--source":
				value, next, err := flagValue(args, index, "--source")
				if err != nil {
					return parsed, err
				}
				if !validSource(value) {
					return parsed, fmt.Errorf("argument --source: invalid choice: %q", value)
				}
				parsed.source = value
				index = next
			case strings.HasPrefix(arg, "--source="):
				value := strings.TrimPrefix(arg, "--source=")
				if !validSource(value) {
					return parsed, fmt.Errorf("argument --source: invalid choice: %q", value)
				}
				parsed.source = value
				index++
			case arg == "--cwd":
				value, next, err := flagValue(args, index, "--cwd")
				if err != nil {
					return parsed, err
				}
				parsed.cwd = value
				index = next
			case strings.HasPrefix(arg, "--cwd="):
				parsed.cwd = strings.TrimPrefix(arg, "--cwd=")
				index++
			case arg == "--session":
				value, next, err := flagValue(args, index, "--session")
				if err != nil {
					return parsed, err
				}
				parsed.session = value
				index = next
			case strings.HasPrefix(arg, "--session="):
				parsed.session = strings.TrimPrefix(arg, "--session=")
				index++
			case arg == "--":
				index++
				if index < len(args) && parsed.command == "" {
					if !validCommand(args[index]) {
						return parsed, fmt.Errorf("invalid choice: %q", args[index])
					}
					parsed.command = args[index]
					index++
				}
			case strings.HasPrefix(arg, "-"):
				return parsed, fmt.Errorf("unrecognized arguments: %s", arg)
			default:
				if !validCommand(arg) {
					return parsed, fmt.Errorf("invalid choice: %q", arg)
				}
				parsed.command = arg
				index++
			}
			continue
		}

		// Subcommand arguments. --source and --cwd must precede the command.
		switch {
		case arg == "-h" || arg == "--help":
			parsed.help = true
			index++
		case parsed.command == "inspect" && arg == "--session":
			value, next, err := flagValue(args, index, "--session")
			if err != nil {
				return parsed, err
			}
			parsed.inspectSession = value
			index = next
		case parsed.command == "inspect" && strings.HasPrefix(arg, "--session="):
			parsed.inspectSession = strings.TrimPrefix(arg, "--session=")
			index++
		case strings.HasPrefix(arg, "-"):
			return parsed, fmt.Errorf("unrecognized arguments: %s", arg)
		default:
			return parsed, fmt.Errorf("unrecognized arguments: %s", arg)
		}
	}
	if parsed.cwd != "" && parsed.session != "" {
		return parsed, fmt.Errorf("argument --session: not allowed with argument --cwd")
	}
	if parsed.command == "inspect" && parsed.inspectSession == "" && !parsed.help {
		return parsed, fmt.Errorf("the following arguments are required: --session")
	}
	return parsed, nil
}

// canonicalOption implements argparse's unambiguous long-option prefix
// matching so accepted baseline syntax is not shrunk.
func canonicalOption(arg string) (string, error) {
	if !strings.HasPrefix(arg, "--") || arg == "--" {
		return arg, nil
	}
	name, value, hasValue := strings.Cut(arg, "=")
	options := []string{"--help", "--version", "--source", "--cwd", "--session"}
	matches := []string{}
	for _, option := range options {
		if strings.HasPrefix(option, name) {
			matches = append(matches, option)
		}
	}
	if len(matches) == 1 {
		if hasValue {
			return matches[0] + "=" + value, nil
		}
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous option: %s", name)
	}
	return arg, nil
}

func flagValue(args []string, index int, name string) (string, int, error) {
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("argument %s: expected one argument", name)
	}
	return args[index+1], index + 2, nil
}

func validSource(value string) bool {
	return value == "all" || value == "codex" || value == "claude"
}

func validCommand(value string) bool {
	return value == "doctor" || value == "sessions" || value == "inspect"
}
