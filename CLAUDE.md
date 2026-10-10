<!-- workflow-manager:begin -->
## Claude WM

This repository is tracked on a Claude WM board. `.taskboard/tasks.json`
is the shared task list: rows with `"requested": true` are work assigned to
you, and you report progress by setting a row's `status` to `in_progress`
when you start and `review` when you open a PR.

Read `.taskboard/README.md` for the full contract, and `.taskboard/example.json`
for a complete valid file, **before** touching `tasks.json`. Nothing in it
is validated: a wrong status, a re-minted `id` or a dropped `tombstones`
key is accepted and quietly means something else. Never create
`tasks.json` yourself — the app writes it.
<!-- workflow-manager:end -->
