# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

This is Celestia's fork of Cosmos SDK v0.50.x (`github.com/cosmos/cosmos-sdk`). CometBFT is replaced with `celestiaorg/celestia-core`. The main upstream remote is `upstream` (celestiaorg/cosmos-sdk), and the primary branch is `release/v0.52.x-celestia`.

Key Celestia-specific changes: lazy migrations for x/staking and x/slashing, CIP-30 support, BlockAPI exposure through app gRPC server, distribution rewards are not auto-claimed, default ABCI gRPC port changed to 36658, default keyring backend changed to `test`.

## Build Commands

```bash
make build              # Build simd binary to ./build/
make install            # Install simd to $GOPATH/bin
make build-linux-amd64  # Cross-compile for Linux amd64
```

## Testing

```bash
# Unit tests (runs across all sub-modules)
make test-unit

# Run a single test
go test -v -run TestName ./path/to/package/...

# Run tests for a specific package
go test ./x/bank/keeper/...

# Integration and E2E tests (from tests/ directory)
make test-integration   # 30m timeout
make test-e2e           # 30m timeout, race detector, requires 'e2e' build tag

# Race detector
make test-race

# Simulation tests
make test-sim-nondeterminism
make test-sim-import-export
```

Test tags used by `make test-unit`: `cgo ledger test_ledger_mock norace`.

## Linting

```bash
make lint               # Lint all sub-modules (installs golangci-lint v2.5.0 if needed)
make lint-fix           # Lint with auto-fix
```

Linting iterates over every `go.mod` in the repo and runs `golangci-lint` with `.golangci.yml`. Generated proto files (`*.pb.go`, `*.pb.gw.go`, `*.pulsar.go`) are excluded.

## Protobuf

```bash
make proto-gen          # Generate protobuf code (requires Docker)
make proto-lint         # Lint proto files
make proto-format       # Format proto files
```

Proto definitions are in `proto/cosmos/` and use Buf (`buf.yaml`). Generation scripts are in `scripts/protocgen.sh` and `scripts/protocgen-pulsar.sh`.

## Multi-Module Repository

The repo contains multiple Go modules with separate `go.mod` files. The root module replaces some via local paths:

* `api/` - API type definitions (`cosmossdk.io/api`)
* `log/` - Logging interface (`cosmossdk.io/log`)
* `core/` - Core interfaces (`cosmossdk.io/core`)
* `math/` - Int/Dec math types (`cosmossdk.io/math`)
* `errors/` - Error types (`cosmossdk.io/errors`)
* `store/` - State store implementations (`cosmossdk.io/store`)
* `collections/` - State collections library (`cosmossdk.io/collections`)
* `depinject/` - Dependency injection framework (`cosmossdk.io/depinject`)
* `simapp/` - Example/test application
* `tests/` - Integration and E2E test suites

When tidying dependencies: `make tidy-all` runs `go mod tidy` across all sub-modules.

## Architecture

**baseapp/** - Core ABCI application framework. Handles message routing, state management, gas metering, and gRPC server integration.

**x/** - Application modules (auth, bank, staking, slashing, gov, distribution, mint, evidence, upgrade, etc.). Each module follows the Cosmos SDK module pattern with keeper, types, client/cli, and migrations sub-packages.

**types/** - Core SDK types: accounts, addresses, coins, context, messages, transactions, module interfaces.

**codec/** - Serialization layer with both Protobuf and Amino (legacy) codecs.

**server/** - Application startup, CometBFT integration, gRPC server management. Includes Celestia-specific BlockAPI proxy service for standalone mode.

**client/** - CLI framework built on Cobra, transaction broadcasting, key management.

**runtime/** - App initialization and module wiring using depinject.

**testutil/** - Test helpers including `DefaultContext()` for in-memory store contexts, `CreateKeyringAccounts()`, CLI test helpers, and network simulation utilities.

**simapp/** - Reference application used for testing. Has two wiring modes: modern depinject (`app.go`) and legacy (`app_v1.go`, enabled with `app_v1` build tag).

## Key Replace Directives

In `go.mod`, CometBFT is replaced with celestia-core:

```go
github.com/cometbft/cometbft => github.com/celestiaorg/celestia-core v0.39.11
```

### Security PRs

- For PRs that resolve Hacken bug bounty reports, do not include details about the bug in the PR description. Instead, link to a Linear issue that contains more details on the bug and the link to the Hacken bug bounty report.
