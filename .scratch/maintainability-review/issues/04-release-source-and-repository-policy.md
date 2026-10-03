Status: resolved

## What to build

Clarify two small maintenance surfaces without introducing a broad architecture change. Rename the release-download abstraction so it reflects support for custom mirrors as well as GitHub, and make the repository tracking policy explicit so required development and release infrastructure cannot disappear silently behind a catch-all ignore rule.

## Acceptance criteria

- [x] The release abstraction is named `ReleaseSource` throughout its implementation, consumers, mocks, and tests.
- [x] The rename does not change download, checksum, version-listing, or latest-version behavior.
- [x] Generated artifacts are ignored explicitly rather than through a repository-wide catch-all rule.
- [x] Required repository infrastructure, including README files, the license, CI/release configuration, documentation, and Markdown metadata, remains trackable unless a documented source-only policy explicitly says otherwise.
- [x] The repository policy is documented clearly enough that adding or removing a required infrastructure file cannot happen silently.
- [x] No package-wide split, dependency-injection framework, or new service/use-case/controller layer is introduced.
- [x] Existing tests and references are updated so the renamed abstraction is used consistently.

## Blocked by

None - can start immediately. Recommended execution order is after the first three tickets.

## Comments

- Renamed the release seam and all Go/test/planning references to `ReleaseSource`.
- Replaced the repository-wide ignore rule with explicit generated/local artifact entries and documented the tracking policy.
- Verified with `go test ./internal/manager` and repository policy assertions.
- Review: Standards PASS; Spec PASS after narrowing the diff to the issue's direct scope.
- Commit: this issue's independent commit is recorded in the repository history and completion report.
