# Repository migration

The active Celestia Cosmos SDK repository is [celestiaorg/cosmos-sdk](https://github.com/celestiaorg/cosmos-sdk). It is an independent GitHub repository. The original fork is preserved at [celestiaorg/cosmos-sdk-archive](https://github.com/celestiaorg/cosmos-sdk-archive).

All 136 issues were transferred natively, retaining 11 open and 125 closed issues, 172 comments and 65 project memberships. Issue numbers and comment IDs changed. All 644 historical pull requests remain in the archive with their original numbers.

## Find historical links

- [Issue map](issues.csv) and [JSON](issues.json): original issue number and URL to current issue URL.
- [Comment map](comments.csv) and [JSON](comments.json): original comment URL and ID to current comment URL and ID.
- [Pull request map](pull-requests.csv) and [JSON](pull-requests.json): original PR URL to the archive URL.

Use the current URLs in these maps. Reusing the original repository name overrides repository rename redirects, so a historical URL containing `cosmos-sdk` and an old issue or PR number can resolve incorrectly or return 404. The `archive_lookup_url` column is an additional lookup path, not a promise that every GitHub transfer redirect survives.

## Preservation notes

Git history, tags and releases were copied. The active default branch removes root `SECURITY.md` to inherit the organization's policy and adds these migration maps and notice. Historical authored issues and comments were not recreated.

GitHub omitted four historical timeline cross-reference events and changed the timestamp or actor on two others. Original events remain in the migration backups; exact timeline preservation is not claimed. Two native attachment rewrites also changed comment edit timestamps. The rewritten images are accessible, but their original bytes were not downloaded before transfer, so byte equality cannot be independently proven. Original text, authors, creation dates and original edit timestamps remain recorded in the backups.

Historical PR discussion stays in the archive. New work belongs in the active repository. Repository renaming does not change existing module declarations or release tags.
