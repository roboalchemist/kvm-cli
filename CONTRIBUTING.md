# Contributing

Thanks for your interest in `kvm-cli`.

## Development setup

```bash
git clone https://github.com/roboalchemist/kvm-cli.git
cd kvm-cli
make build          # ./kvm-cli
make test           # smoke tests (no device or credentials required)
make test-unit      # unit tests with -race and coverage
make lint           # golangci-lint
```

Unit tests are hermetic: integration tests carry a `//go:build integration` tag
and are never compiled by `make test-unit`, so no KVM device or credentials are
needed to contribute.

## Running against a device

```bash
export KVM_URL=https://glkvm.local
export KVM_USERNAME=admin
export KVM_PASSWORD=secret
kvm-cli info --json
```

See [docs/config.md](docs/config.md) for the full configuration reference.

## Guidelines

1. Fork the repository and create a feature branch.
2. Keep changes focused; match the existing style (`gofmt`, `go vet`).
3. Add or update tests. New `pkg/*` code should keep coverage at or above 90%.
4. Ensure `make check` passes before opening a pull request.
5. Describe what changed and how you tested it.

## Commit messages

Use the imperative mood and a scope prefix where helpful: `feat:`, `fix:`,
`docs:`, `test:`, `ci:`.

## Reporting bugs

Open an issue with your OS, `kvm-cli version` output, the exact command, and the
observed vs. expected behaviour.
