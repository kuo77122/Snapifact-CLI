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
	"unicode/utf8"

	"github.com/kuo77122/snapifact-cli/internal/cli"
	"golang.org/x/term"
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
  image      upload a PNG or JPEG image snapshot
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
  SNAPIFACT_API_KEY              optional API key for create requests; required with --password

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
  --password                     prompt securely for a password and confirmation
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

const imageUsageText = `Usage: snapifact image [options] [path|-]

Arguments:
  [path]  zero or one PNG or JPEG file

Rules:
  Omit [path] or use - to read a PNG or JPEG from stdin.
  Options may appear before or after the operand.
  Use -- before a dash-prefixed path.
  Image content is limited to 8 MiB.
  SNAPIFACT_API_KEY is optional for create requests, required with --password, and never sent on delete, view, or raw.
  --description-file - uses stdin for the description, so it is invalid when content also comes from stdin.

` + sharedOptionsText + `Examples:
  snapifact image path/to/photo.png --title "Review" --json
  cat photo.jpg | snapifact image
`

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

type passwordReader func() (password, confirmation string, err error)

var errPasswordValue = errors.New("--password does not accept a value")

var errPasswordUnavailable = errors.New("unable to read password securely")

var errPasswordMismatch = errors.New("password confirmation does not match")

var errPasswordInvalid = errors.New("password must be valid UTF-8 and 12-1024 bytes")

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWithPasswordReader(args, stdin, stdout, stderr, readPasswordFromTTY)
}

func runWithPasswordReader(args []string, stdin io.Reader, stdout, stderr io.Writer, readPassword passwordReader) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 1
	}
	if err := rejectValuedPasswordFlag(args); err != nil {
		fmt.Fprintln(stderr, err)
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
		return runUpload("diff", rest, stdin, stdout, stderr, readPassword)
	case "compare":
		return runCompare(rest, stdin, stdout, stderr, readPassword)
	case "file":
		return runUpload("file", rest, stdin, stdout, stderr, readPassword)
	case "markdown":
		return runUpload("markdown", rest, stdin, stdout, stderr, readPassword)
	case "mermaid":
		return runUpload("mermaid", rest, stdin, stdout, stderr, readPassword)
	case "html":
		return runUpload("html", rest, stdin, stdout, stderr, readPassword)
	case "csv":
		return runUpload("csv", rest, stdin, stdout, stderr, readPassword)
	case "image":
		return runImage(rest, stdin, stdout, stderr, readPassword)
	case "delete":
		return runDelete(rest, stdout, stderr)
	case "version":
		return runVersion(rest, stdout, stderr)
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
		if isValuedPasswordFlag(arg) {
			return nil, errPasswordValue
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

func rejectValuedPasswordFlag(args []string) error {
	for _, arg := range args {
		if arg == "--" {
			return nil
		}
		if isValuedPasswordFlag(arg) {
			return errPasswordValue
		}
	}
	return nil
}

func isValuedPasswordFlag(arg string) bool {
	return strings.HasPrefix(arg, "--password=") || strings.HasPrefix(arg, "-password=")
}

func runCompare(args []string, stdin io.Reader, stdout, stderr io.Writer, readPassword passwordReader) int {
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), compareUsageText) }
	jsonMode := flags.Bool("json", false, "output full JSON response")
	title := flags.String("title", "", "snapshot title")
	descFile := flags.String("description-file", "", "path to markdown description file (use - for stdin)")
	passwordRequested := flags.Bool("password", false, "prompt securely for a password and confirmation")
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
	password, err := collectPassword(*passwordRequested, readPassword)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	serverURL := cli.ServerURL()
	tokenDir := cli.TokenDir(os.Getenv("SNAPIFACT_STATE_DIR"))
	cli.CleanStaleTokens(tokenDir)
	response, err := cli.CreateCompareSnapshotWithDescriptionAndPassword(serverURL, *title, string(before), filepath.Base(beforePath), string(after), filepath.Base(afterPath), description, password)
	if err != nil {
		writeCLIError(stderr, err, password)
		return 1
	}
	return finishCreate(serverURL, tokenDir, response, *jsonMode, stdout, stderr, password)
}

// runUpload reads content (and optional description) and sends a snapshot to the server.
func runUpload(contentType string, args []string, stdin io.Reader, stdout, stderr io.Writer, readPassword passwordReader) int {
	flags := flag.NewFlagSet(contentType, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), uploadUsageText(contentType)) }
	jsonMode := flags.Bool("json", false, "output full JSON response")
	title := flags.String("title", "", "snapshot title")
	descFile := flags.String("description-file", "", "path to markdown description file (use - for stdin)")
	passwordRequested := flags.Bool("password", false, "prompt securely for a password and confirmation")

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
	password, err := collectPassword(*passwordRequested, readPassword)
	if err != nil {
		fmt.Fprintln(stderr, err)
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
	resp, err := cli.CreateSnapshotWithDescriptionAndFilenameAndPassword(serverURL, contentType, *title, content, filename, description, password)
	if err != nil {
		writeCLIError(stderr, err, password)
		return 1
	}
	return finishCreate(serverURL, tokenDir, resp, *jsonMode, stdout, stderr, password)
}

func runImage(args []string, stdin io.Reader, stdout, stderr io.Writer, readPassword passwordReader) int {
	flags := flag.NewFlagSet("image", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), imageUsageText) }
	jsonMode := flags.Bool("json", false, "output full JSON response")
	title := flags.String("title", "", "snapshot title")
	descFile := flags.String("description-file", "", "path to markdown description file (use - for stdin)")
	passwordRequested := flags.Bool("password", false, "prompt securely for a password and confirmation")

	operands, err := parseSnapshotArgs(flags, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if len(operands) > 1 {
		fmt.Fprintln(stderr, "usage: snapifact image [options] [path|-]")
		return 1
	}

	contentFromStdin := len(operands) == 0 || operands[0] == "-"
	if contentFromStdin && *descFile == "-" {
		fmt.Fprintln(stderr, "error: cannot read both content and description from stdin; provide a file path for one of them")
		return 1
	}

	content, err := readImageContent(operands, contentFromStdin, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "read image: %v\n", err)
		return 1
	}
	description, err := readDescription(*descFile, contentFromStdin, stdin, stderr)
	if err != nil {
		return 1
	}
	password, err := collectPassword(*passwordRequested, readPassword)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	serverURL := cli.ServerURL()
	tokenDir := cli.TokenDir(os.Getenv("SNAPIFACT_STATE_DIR"))
	cli.CleanStaleTokens(tokenDir)
	filename := ""
	if !contentFromStdin {
		filename = filepath.Base(operands[0])
	}
	response, err := cli.CreateBinarySnapshotWithPassword(serverURL, *title, content, filename, description, password)
	if err != nil {
		writeCLIError(stderr, err, password)
		return 1
	}
	return finishCreate(serverURL, tokenDir, response, *jsonMode, stdout, stderr, password)
}

func readImageContent(operands []string, contentFromStdin bool, stdin io.Reader) ([]byte, error) {
	var reader io.Reader = stdin
	var file *os.File
	if !contentFromStdin {
		var err error
		file, err = os.Open(operands[0])
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	content, err := io.ReadAll(io.LimitReader(reader, cli.MaxImageContentSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > cli.MaxImageContentSize {
		return nil, fmt.Errorf("content exceeds 8 MiB limit")
	}
	return content, nil
}

func writeCLIError(stderr io.Writer, err error, secrets ...string) {
	var errResp *cli.ErrorResponse
	if errors.As(err, &errResp) {
		out, _ := json.Marshal(errResp)
		fmt.Fprintln(stderr, sanitizeText(string(out), secrets...))
	} else {
		fmt.Fprintf(stderr, "error: %s\n", sanitizeError(err, secrets...))
	}
}

func finishCreate(serverURL, tokenDir string, resp *cli.CreateResponse, jsonMode bool, stdout, stderr io.Writer, password string) int {
	resp = redactCreateResponse(resp, password)
	if os.Getenv("SNAPIFACT_API_KEY") != "" && !acceptedKeyedTier(resp.Tier) {
		if err := cli.DeleteSnapshot(serverURL, resp.ID, resp.DeleteToken); err == nil {
			fmt.Fprintln(stderr, "error: server did not apply the configured API key; snapshot was deleted")
			return 1
		} else {
			deleteErr := err
			if saveErr := cli.SaveToken(tokenDir, resp.ID, resp.DeleteToken, resp.ExpiresAt); saveErr == nil {
				fmt.Fprintln(stderr, "WARNING: server did not apply the configured API key and compensating delete failed.")
				fmt.Fprintf(stderr, "Snapshot URL: %s\n", resp.URL)
				fmt.Fprintln(stderr, "Snapshot delete token saved locally.")
				fmt.Fprintf(stderr, "Delete error: %s\n", sanitizeError(deleteErr, resp.DeleteToken, password))
				return 1
			} else {
				fmt.Fprintln(stderr, "WARNING: server did not apply the configured API key and also failed to revoke the snapshot.")
				fmt.Fprintf(stderr, "Snapshot URL: %s\n", resp.URL)
				fmt.Fprintf(stderr, "Snapshot delete token: %s\n", resp.DeleteToken)
				fmt.Fprintf(stderr, "Save error: %v\n", saveErr)
				fmt.Fprintf(stderr, "Delete error: %v\n", sanitizeError(deleteErr, password))
				return 1
			}
		}
	}

	if err := cli.SaveToken(tokenDir, resp.ID, resp.DeleteToken, resp.ExpiresAt); err != nil {
		// Token-save failure: attempt compensating delete
		if delErr := cli.DeleteSnapshot(serverURL, resp.ID, resp.DeleteToken); delErr != nil {
			// Compensating delete also failed — print recovery warning
			fmt.Fprintf(stderr, "WARNING: failed to save delete token locally and also failed to revoke the snapshot on the server.\n")
			fmt.Fprintf(stderr, "Snapshot URL: %s\n", resp.URL)
			fmt.Fprintf(stderr, "Snapshot delete token: %s\n", resp.DeleteToken)
			fmt.Fprintf(stderr, "Save error: %v\n", err)
			fmt.Fprintf(stderr, "Delete error: %v\n", sanitizeError(delErr, password))
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

func redactCreateResponse(resp *cli.CreateResponse, password string) *cli.CreateResponse {
	if resp == nil || password == "" {
		return resp
	}
	redact := func(value string) string { return strings.ReplaceAll(value, password, "[REDACTED]") }
	copy := *resp
	copy.ID = redact(copy.ID)
	copy.URL = redact(copy.URL)
	copy.ExpiresAt = redact(copy.ExpiresAt)
	copy.DeleteToken = redact(copy.DeleteToken)
	copy.Tier = redact(copy.Tier)
	return &copy
}

func acceptedKeyedTier(tier string) bool {
	switch tier {
	case "basic", "pro", "admin":
		return true
	default:
		return false
	}
}

func sanitizeError(err error, extra ...string) string {
	return sanitizeTextWithSecrets(err.Error(), extra...)
}

func sanitizeText(text string, extra ...string) string {
	return sanitizeTextWithSecrets(text, extra...)
}

func sanitizeTextWithSecrets(text string, extra ...string) string {
	if apiKey := os.Getenv("SNAPIFACT_API_KEY"); apiKey != "" {
		text = strings.ReplaceAll(text, apiKey, "[REDACTED]")
	}
	for _, secret := range extra {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return text
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

func collectPassword(requested bool, reader passwordReader) (string, error) {
	if !requested {
		return "", nil
	}
	if os.Getenv("SNAPIFACT_API_KEY") == "" {
		return "", errors.New("SNAPIFACT_API_KEY is required with --password")
	}
	if reader == nil {
		return "", errPasswordUnavailable
	}

	password, confirmation, err := reader()
	if err != nil {
		return "", errPasswordUnavailable
	}
	if password != confirmation {
		return "", errPasswordMismatch
	}
	if !utf8.ValidString(password) || !utf8.ValidString(confirmation) || len([]byte(password)) < 12 || len([]byte(password)) > 1024 {
		return "", errPasswordInvalid
	}
	return password, nil
}

func readPasswordFromTTY() (string, string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", "", err
	}
	defer tty.Close()
	if !term.IsTerminal(int(tty.Fd())) {
		return "", "", errors.New("controlling terminal is not a terminal")
	}

	if _, err := fmt.Fprint(tty, "Password: "); err != nil {
		return "", "", err
	}
	password, err := term.ReadPassword(int(tty.Fd()))
	if _, newlineErr := fmt.Fprintln(tty); err != nil {
		return "", "", err
	} else if newlineErr != nil {
		return "", "", newlineErr
	}
	if _, err := fmt.Fprint(tty, "Confirm password: "); err != nil {
		return "", "", err
	}
	confirmation, err := term.ReadPassword(int(tty.Fd()))
	if _, newlineErr := fmt.Fprintln(tty); err != nil {
		return "", "", err
	} else if newlineErr != nil {
		return "", "", newlineErr
	}
	return string(password), string(confirmation), nil
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

func runVersion(args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(stdout, versionUsageText)
			return 0
		}
		if arg == "--password" || arg == "-password" {
			fmt.Fprintln(stderr, "version does not support --password")
			return 1
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
