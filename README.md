# Snapifact CLI

[![CI](https://github.com/kuo77122/Snapifact-CLI/actions/workflows/ci.yml/badge.svg)](https://github.com/kuo77122/Snapifact-CLI/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/kuo77122/Snapifact-CLI?display_name=tag&label=latest%20release)](https://github.com/kuo77122/Snapifact-CLI/releases/latest)
[![MIT license](https://img.shields.io/github/license/kuo77122/Snapifact-CLI)](LICENSE)

CLI for turning local artifacts into temporary browser links for human review.
Snapifact is built for AI-agent developers who need to share files, Markdown,
diffs, diagrams, comparisons, HTML, or CSV with a person without leaving the
local workflow.

## Quick start

The release installer is the primary installation path for supported Linux and
macOS systems on amd64 or arm64:

```sh
curl -fsSL https://snapifact.dev/downloads/latest/install.sh | sh
```

The installer verifies the downloaded binary against its SHA-256 manifest and
installs `snapifact` to `$HOME/.local/bin` by default. [View the latest
checksums](https://snapifact.dev/downloads/latest/SHA256SUMS) before installing.

Alternatively, install the latest Go module version:

```sh
go install github.com/kuo77122/snapifact-cli/cmd/snapifact@latest
```

Create a review link:

```sh
snapifact markdown README.md --title "README review"
```

## Trust boundary

- **No account:** no signup, email, or API key is required.
- Links are **unlisted, not private**: anyone with the link can view them.
- Content is **readable by the operator**. Never upload secrets.
- Snapshots expire after a **seven-day expiry** and can be deleted earlier
  with the local delete token.

## Commands

| Command | Purpose | Input |
| --- | --- | --- |
| `snapifact diff` | Upload a unified diff snapshot | One file or stdin |
| `snapifact compare` | Compare two UTF-8 files | Two file paths |
| `snapifact text` | Upload UTF-8 plain text or source code | One file or stdin |
| `snapifact markdown` | Upload a Markdown snapshot | One file or stdin |
| `snapifact mermaid` | Upload a Mermaid diagram snapshot | One file or stdin |
| `snapifact html` | Upload a sandboxed HTML snapshot | One file or stdin |
| `snapifact csv` | Upload a CSV snapshot | One file or stdin |
| `snapifact image` | Upload a PNG or JPEG image snapshot | One file or stdin (8 MiB maximum) |
| `snapifact delete` | Delete a snapshot | Snapshot ID or URL |
| `snapifact version` | Print the CLI version | — |

Global flags are also available:

- `snapifact --help` or `snapifact -h` prints the command list.
- `snapifact --version` prints the CLI version.

The upload and compare commands support these options:

- `--title TEXT` sets a snapshot title.
- `--description-file PATH` reads a Markdown description file. Use `-` to read
  the description from stdin when the artifact itself comes from a file.
- `--json` prints the complete create response instead of only the review URL.
- `--password` securely prompts for a password and confirmation from the
  controlling terminal. It requires `SNAPIFACT_API_KEY`; the password is never
  read from stdin, argv, environment, or a file. Passwords must be valid UTF-8
  and 12–1024 bytes.

For image uploads, set the optional `SNAPIFACT_API_KEY` environment variable to
apply a basic, pro, or admin API key to create requests, including image
creation:

```sh
SNAPIFACT_API_KEY="$SNAPIFACT_API_KEY" snapifact image photo.png
```

To create a password-protected snapshot, add `--password` to any create or
compare command. The API key is required and the CLI prompts twice without
echoing the password:

```sh
SNAPIFACT_API_KEY="$SNAPIFACT_API_KEY" snapifact markdown README.md --password
```

The key is never sent on delete, view, or raw requests. Image input is limited
to 8 MiB; the server remains authoritative for image validation.

Content can be read from stdin with `-` or by omitting the path:

```sh
cat report.md | snapifact markdown -
cat photo.jpg | snapifact image
snapifact compare before.txt after.txt --title "Before and after"
snapifact markdown report.md --description-file notes.md
snapifact delete https://snapifact.dev/v/<snapshot-id>
```

Options may appear before, after, or between path operands. Use `--` before a
path that starts with `-`:

```sh
snapifact text -- -report.txt
```

Run `snapifact <command> --help` for the command's built-in usage text.

## Development

This repository uses the same checks as CI:

```sh
go test ./...
go vet ./...
go build ./cmd/snapifact
```

## Contributing

Read the [open issues](https://github.com/kuo77122/Snapifact-CLI/issues), then
open an issue or [pull request](https://github.com/kuo77122/Snapifact-CLI/pulls)
with a focused change and the checks you ran.

## Public links

- [Snapifact](https://snapifact.dev)
- [Privacy](https://snapifact.dev/privacy)
- [Security](https://snapifact.dev/security)
- [Releases](https://github.com/kuo77122/Snapifact-CLI/releases)
- [MIT license](LICENSE)
