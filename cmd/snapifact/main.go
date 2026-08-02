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

commands:
	  diff       upload a unified diff snapshot
	  compare    compare two UTF-8 files
  file       upload a file snapshot
  markdown   upload a markdown snapshot
  mermaid    upload a Mermaid diagram snapshot
  html       upload a sandboxed HTML snapshot
  csv        upload a CSV snapshot
  delete     delete a snapshot
  version    print the CLI version

run 'snapifact <command> --help' for per-command flags
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
		fmt.Fprintf(stdout, "snapifact %s\n", cliVersion())
		return 0
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

func runCompare(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonMode := flags.Bool("json", false, "output full JSON response")
	title := flags.String("title", "", "snapshot title")
	descFile := flags.String("description-file", "", "path to markdown description file (use - for stdin)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: snapifact compare [options] <before-file> <after-file>")
		return 1
	}
	beforePath, afterPath := flags.Arg(0), flags.Arg(1)
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
	jsonMode := flags.Bool("json", false, "output full JSON response")
	title := flags.String("title", "", "snapshot title")
	descFile := flags.String("description-file", "", "path to markdown description file (use - for stdin)")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	// Detect ambiguous stdin BEFORE reading anything:
	// content from stdin and description from stdin is ambiguous.
	contentFromStdin := flags.NArg() == 0 || flags.Arg(0) == "-"
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
		path := flags.Arg(0)
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
		filename = filepath.Base(flags.Arg(0))
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
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() == 0 {
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
