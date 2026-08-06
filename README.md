# Snapifact CLI

Standalone command-line client for creating and deleting Snapifact snapshots.

## Install

```sh
go install github.com/kuo77122/snapifact-cli/cmd/snapifact@latest
```

## Usage

```sh
snapifact file report.txt
snapifact markdown README.md --title "Notes"
snapifact delete https://snapifact.dev/v/<snapshot-id>
```

Snapshot options may appear before, after, or between path operands. Use `--`
before a path that starts with `-`:

```sh
snapifact markdown --title "Notes" README.md
snapifact markdown README.md --title "Notes" --json
snapifact file -- -report.txt
```

Content may also be read from standard input. Use `--json` for the complete
create response. Delete tokens are stored locally with restricted permissions.

## Development

```sh
go test ./...
go vet ./...
go build ./cmd/snapifact
```

The client uses the Snapifact HTTP API at `https://api.snapifact.dev`.
