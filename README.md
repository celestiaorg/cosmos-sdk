# Cosmos SDK for Celestia

This is Celestia's independent Cosmos SDK repository. [Historical pull requests](https://github.com/celestiaorg/cosmos-sdk-archive/pulls) remain in the original fork. See the [repository migration guide and issue/comment maps](docs/repository-migration/README.md).

It tracks Cosmos SDK v0.52.x and carries the following changes compared to upstream `cosmos/cosmos-sdk`:

* Store app version in consensus param store
* Modify continuous vesting account to add start time
* Re-add query router for custom abci queries
* Add v0.52 helpers to facilitate testing
* Disable heavy bank migrations
* Backport improvements for DOS protection for x/authz
* Support historical account number queries
* Support [CIP-30](https://github.com/celestiaorg/CIPs/blob/main/cips/cip-030.md)
* The `prove` flag for is set to `false` for tx queries, similarly to celestia/cosmos-sdk v0.46
* The x/staking migration for delegation keys has been made a lazy migration
* The x/staking migration for historical info keys has been made a lazy migration
* The x/slashing migration for validator missed block bitmap has been made a lazy migration
* The x/slashing key prefix for validator missed block bitmap has been changed from `0x02` to `0x12`
* The celestia-core celestia-core `BlockAPI` is exposed through the app grpc server. When running in standalone mode the app uses a proxy service to maintain support through same the app grpc port.
* The default listen address for remote ABCI connections over grpc has been updated from `tcp://127.0.0.1:26658` to `tcp://127.0.0.1:36658`.
* The default keyring backend has been changed from `os` to `test`.
* Rewards from the x/distribution module are not auto-claimed. Instead, they can be claimed at arbitrary points in time.
* Extended `TxResponse` to include `signers` field with the list of transaction signers

Read the [CHANGELOG.md](CHANGELOG.md) for more details.

## Contributing

PR titles must follow [conventional commits](https://www.conventionalcommits.org/en/v1.0.0/) (e.g. `fix:`, `feat:`, `chore:`, `docs:`) and include `!` (e.g. `feat!:`) for breaking changes. The subject must start with `[high]`, `[medium]`, or `[low]` to indicate the review thoroughness needed. For example: `fix(x/staking)!: [high] reject weak consensus keys` or `docs: [low] clarify setup instructions`. Include the same tag in the PR description and explain why you chose it.

* `[high]`: Deep review for consensus, state transitions, security, or other changes with significant risk (e.g. `x/` module state machine logic, baseapp/ABCI, store, ante handlers, migrations, consensus-breaking changes).
* `[medium]`: Standard review for code changes with limited impact and well-understood behavior.
* `[low]`: Focused review for straightforward docs, tests, or tooling changes with low risk.

Choose the higher level when unsure. A small diff can still need deep review. CI checks the title format; reviewers should confirm the chosen level.

Dependabot defaults to `[high]` for production Go module dependencies (security updates only) and `[medium]` for GitHub Actions and test or tooling modules. Maintainers should confirm the level and add it to the PR description with a brief explanation.

## Audits

| Date       | Auditor                                       | Version                                                                                              | Report                              |
|------------|-----------------------------------------------|------------------------------------------------------------------------------------------------------|-------------------------------------|
| 2025/04/22 | [Informal Systems](https://informal.systems/) | [dba8171](https://github.com/celestiaorg/cosmos-sdk/commit/dba8171d9f829d90134b1669468831625ee89b0e) | [cip-31.pdf](docs/audit/cip-31.pdf) |
