// Command snapifact provides the agent-facing CLI for uploading and
// deleting Snapifact snapshots.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/kuo77122/snapifact-cli/internal/cli"
)

const usageText = `usage: snapifact <command> [options]

Commands:
  diff       upload a unified diff snapshot
  compare    compare two UTF-8 files
  file       upload a file snapshot
  markdown   upload a markdown snapshot
  mermaid    upload a Mermaid diagram snapshot
  html       upload a sandboxed HTML snapshot
  csv        upload a CSV snapshot
  delete     delete a snapshot
  version    print the CLI version

Rules:
  Run 'snapifact <command> --help' for complete command syntax and examples.
  For uploads, omit [path] or use - to read content from stdin.
  Options may appear before or after operands for uploads and compare.
  Use -- before a dash-prefixed path so it is treated as an operand.
  compare requires exactly two UTF-8 file operands; delete requires one ID or URL.

Options:
  --version  print the CLI version and exit
  --title <text>                 set the snapshot title
  --description-file <path|->    read the markdown description from path; - reads it from stdin
  --json                         output the full JSON response instead of only the snapshot URL

  --description-file - reads the description from stdin and cannot be combined with content also read from stdin.

Examples:
  printf 'content\n' | snapifact markdown
  snapifact file path/to/file.txt --title "Review" --json
  snapifact compare before.txt after.txt
  snapifact delete kpm2q6xxyegw5czekhga
`

const sharedOptionsText = `Options:
  --title <text>                 set the snapshot title
  --description-file <path|->    read the markdown description from path; - reads it from stdin
  --json                         output the full JSON response instead of only the snapshot URL
`

const versionUsageText = `Usage: snapifact version

Arguments: none

Description:
  Print the CLI version. Use --help to show this help without printing the version.

Examples:
  snapifact version
  snapifact version --help
`

const deleteUsageText = `Usage: snapifact delete <id-or-url>

Arguments:
  <id-or-url>  exactly one snapshot ID or snapshot URL

Rules:
  Exactly one snapshot ID or URL is required.

Examples:
  snapifact delete kpm2q6xxyegw5czekhga
  snapifact delete https://view.test/v/kpm2q6xxyegw5czekhga
`

func uploadUsageText(contentType string) string {
	return fmt.Sprintf(`Usage: snapifact %s [options] [path]

Arguments:
  [path]  zero or one content file

Rules:
  Omit [path] or use - to read content from stdin.
  Options may appear before or after operands.
  Use -- before a dash-prefixed path.
  --description-file - uses stdin for the description, so it is invalid when content also comes from stdin.

%sExamples:
  snapifact %s path/to/content --title "Review" --json
  printf 'content\n' | snapifact %s
  snapifact %s -- --content-file
  snapifact %s --description-file - path/to/content
  Invalid: printf 'content\n' | snapifact %s --description-file -
`, contentType, sharedOptionsText, contentType, contentType, contentType, contentType, contentType)
}

const compareUsageText = `Usage: snapifact compare [options] <before-file> <after-file>

Arguments:
  <before-file> <after-file>  exactly two UTF-8 file operands

Rules:
  Exactly two UTF-8 file operands are required; compare does not read content from stdin.
  The operand - is a file path, not a stdin shorthand.
  Options may appear before or after operands.
  Use -- before dash-prefixed paths.

` + sharedOptionsText + `Examples:
  snapifact compare before.txt after.txt
  snapifact compare before.txt --title "Review" after.txt --json
  snapifact compare -- -before.txt -after.txt
`

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 1
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "--help", "-h":
		fmt.Fprint(stdout, usageText)
		return 0
	case "--version":
		fmt.Fprintf(stdout, "snapifact %s\n", cliVersion())
		return 0
	case "diff":
		return runUpload("diff", rest, stdin, stdout, stderr)
	case "compare":
		return runCompare(rest, stdin, stdout, stderr)
	case "file":
		return runUpload("file", rest, stdin, stdout, stderr)
	case "markdown":
		return runUpload("markdown", rest, stdin, stdout, stderr)
	case "mermaid":
		return runUpload("mermaid", rest, stdin, stdout, stderr)
	case "html":
		return runUpload("html", rest, stdin, stdout, stderr)
	case "csv":
		return runUpload("csv", rest, stdin, stdout, stderr)
	case "delete":
		return runDelete(rest, stdout, stderr)
	case "version":
		return runVersion(rest, stdout)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n%s", cmd, usageText)
		return 1
	}
}

func cliVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

type boolFlag interface {
	IsBoolFlag() bool
}

func parseSnapshotArgs(flags *flag.FlagSet, args []string) ([]string, error) {
	options := make([]string, 0, len(args))
	operands := make([]string, 0, len(args))
	parsingOptions := true

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if parsingOptions && arg == "--" {
			options = append(options, arg)
			parsingOptions = false
			continue
		}
		if !parsingOptions || len(arg) < 2 || arg[0] != '-' || arg == "-" {
			operands = append(operands, arg)
			continue
		}

		options = append(options, arg)
		name := strings.TrimLeft(arg, "-")
		if equals := strings.IndexByte(name, '='); equals >= 0 {
			name = name[:equals]
		}
		registered := flags.Lookup(name)
		if registered != nil && !isBoolFlag(registered.Value) && !strings.Contains(arg, "=") && i+1 == len(args) {
			if err := flags.Parse(options); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("flag needs an argument: -%s", name)
		}
		if registered != nil && !isBoolFlag(registered.Value) && !strings.Contains(arg, "=") && i+1 < len(args) {
			options = append(options, args[i+1])
			i++
		}
	}

	normalized := append(options, operands...)
	if err := flags.Parse(normalized); err != nil {
		return nil, err
	}
	return flags.Args(), nil
}

func isBoolFlag(value flag.Value) bool {
	boolValue, ok := value.(boolFlag)
	return ok && boolValue.IsBoolFlag()
}

func runCompare(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), compareUsageText) }
	jsonMode := flags.Bool("json", false, "output full JSON response")
	title := flags.String("title", "", "snapshot title")
	descFile := flags.String("description-file", "", "path to markdown description file (use - for stdin)")
	operands, err := parseSnapshotArgs(flags, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if len(operands) != 2 {
		fmt.Fprintln(stderr, "usage: snapifact compare [options] <before-file> <after-file>")
		return 1
	}
	beforePath, afterPath := operands[0], operands[1]
	before, err := os.ReadFile(beforePath)
	if err != nil {
		fmt.Fprintf(stderr, "read before file: %v\n", err)
		return 1
	}
	after, err := os.ReadFile(afterPath)
	if err != nil {
		fmt.Fprintf(stderr, "read after file: %v\n", err)
		return 1
	}
	description, err := readDescription(*descFile, false, stdin, stderr)
	if err != nil {
		return 1
	}
	serverURL := cli.ServerURL()
	tokenDir := cli.TokenDir(os.Getenv("SNAPIFACT_STATE_DIR"))
	cli.CleanStaleTokens(tokenDir)
	response, err := cli.CreateCompareSnapshotWithDescription(serverURL, *title, string(before), filepath.Base(beforePath), string(after), filepath.Base(afterPath), description)
	if err != nil {
		writeCLIError(stderr, err)
		return 1
	}
	return finishCreate(serverURL, tokenDir, response, *jsonMode, stdout, stderr)
}

// runUpload reads content (and optional description) and sends a snapshot to the server.
func runUpload(contentType string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(contentType, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), uploadUsageText(contentType)) }
	jsonMode := flags.Bool("json", false, "output full JSON response")
	title := flags.String("title", "", "snapshot title")
	descFile := flags.String("description-file", "", "path to markdown description file (use - for stdin)")

	operands, err := parseSnapshotArgs(flags, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if len(operands) > 1 {
		fmt.Fprintln(stderr, "usage: snapifact "+contentType+" [options] [path]")
		return 1
	}

	// Detect ambiguous stdin BEFORE reading anything:
	// content from stdin and description from stdin is ambiguous.
	contentFromStdin := len(operands) == 0 || operands[0] == "-"
	if contentFromStdin && *descFile == "-" {
		fmt.Fprintf(stderr, "error: cannot read both content and description from stdin; provide a file path for one of them\n")
		return 1
	}

	// Read content from file path or stdin
	var content string
	switch {
	case contentFromStdin:
		data, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "read stdin: %v\n", err)
			return 1
		}
		content = string(data)
	default:
		path := operands[0]
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "read file: %v\n", err)
			return 1
		}
		content = string(data)
	}

	// Read description from --description-file
	description, err := readDescription(*descFile, contentFromStdin, stdin, stderr)
	if err != nil {
		return 1
	}

	serverURL := cli.ServerURL()
	stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
	tokenDir := cli.TokenDir(stateDir)

	// Opportunistically clean stale tokens
	cli.CleanStaleTokens(tokenDir)

	// Send to server
	filename := ""
	if contentType == "csv" && !contentFromStdin {
		filename = filepath.Base(operands[0])
	}
	resp, err := cli.CreateSnapshotWithDescriptionAndFilename(serverURL, contentType, *title, content, filename, description)
	if err != nil {
		writeCLIError(stderr, err)
		return 1
	}
	return finishCreate(serverURL, tokenDir, resp, *jsonMode, stdout, stderr)
}

func writeCLIError(stderr io.Writer, err error) {
	var errResp *cli.ErrorResponse
	if errors.As(err, &errResp) {
		out, _ := json.Marshal(errResp)
		fmt.Fprintln(stderr, string(out))
	} else {
		fmt.Fprintf(stderr, "error: %v\n", err)
	}
}

func finishCreate(serverURL, tokenDir string, resp *cli.CreateResponse, jsonMode bool, stdout, stderr io.Writer) int {
	if err := cli.SaveToken(tokenDir, resp.ID, resp.DeleteToken); err != nil {
		// Token-save failure: attempt compensating delete
		if delErr := cli.DeleteSnapshot(serverURL, resp.ID, resp.DeleteToken); delErr != nil {
			// Compensating delete also failed — print recovery warning
			fmt.Fprintf(stderr, "WARNING: failed to save delete token locally and also failed to revoke the snapshot on the server.\n")
			fmt.Fprintf(stderr, "Snapshot URL: %s\n", resp.URL)
			fmt.Fprintf(stderr, "Snapshot delete token: %s\n", resp.DeleteToken)
			fmt.Fprintf(stderr, "Save error: %v\n", err)
			fmt.Fprintf(stderr, "Delete error: %v\n", delErr)
		} else {
			// Compensating delete succeeded — no URL, non-zero exit
			fmt.Fprintf(stderr, "error: snapshot created but failed to save delete token locally; snapshot was revoked on the server.\n")
			fmt.Fprintf(stderr, "token save error: %v\n", err)
		}
		return 1
	}

	// Success output
	if jsonMode {
		out, _ := json.Marshal(resp)
		fmt.Fprintln(stdout, string(out))
	} else {
		fmt.Fprintln(stdout, resp.URL)
	}
	return 0
}

// readDescription reads the description from the --description-file path.
// Callers must check for ambiguous stdin before calling this function.
func readDescription(descFile string, contentFromStdin bool, stdin io.Reader, stderr io.Writer) (string, error) {
	if descFile == "" {
		return "", nil
	}

	if descFile == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "read description from stdin: %v\n", err)
			return "", err
		}
		return string(data), nil
	}

	data, err := os.ReadFile(descFile)
	if err != nil {
		fmt.Fprintf(stderr, "read description file: %v\n", err)
		return "", err
	}
	return string(data), nil
}

func runDelete(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("delete", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), deleteUsageText) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: snapifact delete <id-or-url>")
		return 1
	}

	idOrURL := flags.Arg(0)
	id := extractID(idOrURL)
	if id == "" {
		fmt.Fprintf(stderr, "invalid snapshot ID or URL: %s\n", idOrURL)
		return 1
	}

	serverURL := cli.ServerURL()
	stateDir := os.Getenv("SNAPIFACT_STATE_DIR")
	tokenDir := cli.TokenDir(stateDir)

	token, err := cli.ReadToken(tokenDir, id)
	if err != nil {
		// No local token — snapshot is already gone from our perspective.
		return 0
	}

	if err := cli.DeleteSnapshot(serverURL, id, token); err != nil {
		var errResp *cli.ErrorResponse
		if errors.As(err, &errResp) {
			out, _ := json.Marshal(errResp)
			fmt.Fprintln(stderr, string(out))
		} else {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return 1
	}

	// On success (204) or already gone (404), remove local token
	_ = cli.RemoveToken(tokenDir, id)
	return 0
}

func runVersion(args []string, stdout io.Writer) int {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(stdout, versionUsageText)
			return 0
		}
	}
	fmt.Fprintf(stdout, "snapifact %s\n", cliVersion())
	return 0
}

// extractID extracts a snapshot ID from a raw ID or a URL.
func extractID(s string) string {
	// If it starts with http, try to extract the last path segment
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		parts := strings.Split(strings.TrimRight(s, "/"), "/")
		if len(parts) > 0 {
			s = parts[len(parts)-1]
		}
	}
	if cli.ValidSnapshotID(s) {
		return s
	}
	return ""
}
