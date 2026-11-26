# Contributing

Bug reports and pull requests are welcome.

## Build and test

Go 1.26 or newer (see `go.mod`).

```sh
go vet ./...
go test ./...
```

Tests need no network, Ollama or cloud account. Run them with an empty `HOME` if you have a real `~/.config/specs-index/config.json`, so your settings do not leak into the tests.

## Pull requests

- Keep a change to one topic, with tests for new behaviour.
- Commit messages: a short imperative subject, then a body if the reason is not obvious.
- `scripts/gen-notices.sh` must be re-run when a dependency changes, and the result committed.
- Security issues go through the process in [SECURITY.md](SECURITY.md), not a public issue.

By contributing you agree that your contribution is licensed under the MIT license of this project.
