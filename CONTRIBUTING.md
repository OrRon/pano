# Contributing to pano

Thanks for helping make pano better.

## Development setup

```sh
brew install go golangci-lint      # Go ≥ 1.27
git clone https://github.com/orron/pano && cd pano
make build                          # → bin/pano
make test                           # go vet + go test -race ./...
make lint                           # golangci-lint
make fuzz                           # short fuzz pass over the parsers
```

Run a dev daemon in the foreground: `bin/pano start --foreground --port 9091`.

## Layout

- `cmd/pano` — the CLI entrypoint (cobra).
- `internal/proxy`, `internal/ca` — the MITM engine and certificate authority.
- `internal/store`, `internal/flow`, `internal/bus` — capture model, ring buffer, SQLite, events.
- `internal/rules` — live traffic rules and breakpoints.
- `internal/control`, `internal/client`, `internal/api` — the control API the CLI and MCP server both use.
- `internal/mcpserver`, `internal/view`, `internal/explain` — the agent-facing surface.
- `docs/` — architecture, MCP catalog, rules, FAQ, ADRs.

Everything is under `internal/`; there is no public Go API yet.

## Pull requests

1. Open an issue first for anything larger than a bug fix.
2. Keep PRs focused. One logical change per PR.
3. `make test lint` must pass. New code needs tests; parsers need fuzz seeds.
4. Use [Conventional Commits](https://www.conventionalcommits.org): `feat(proxy): …`, `fix(ca): …`.
5. Document exported identifiers. Every package has a `doc.go`.
6. Never log or store the CA private key. Never widen the default bind address.

## Design decisions

Load-bearing decisions are recorded in `docs/adr/`. If you want to change one, open an
issue referencing the ADR.
