# Visual QA harness

Screenshots the admin console against a real local Server with seeded data, so a
redesign can be reviewed visually in both themes and both viewports.

```
cd web && npm run build            # the Server embeds web/dist
node scripts/visual-qa/run.mjs --locale en-US
```

| Flag | Meaning |
|---|---|
| `--locale zh-CN\|en-US` | Language of the screenshots (default `zh-CN`) |
| `--agents N` / `--online N` | How many agents to seed, and how many really connect (default 40 / 8) |
| `--only <page>` | One page: `login`, `dashboard`, `agents`, `tokens`, `audit-logs` |
| `--skip-build` | Reuse the binaries from a previous run |
| `--keep` | Leave the Server running afterwards and print the admin login |

Environment: `TM_QA_OUT` (screenshot directory), `TM_QA_DIR` (work directory),
`TM_QA_PORT`, `TM_QA_CDP_PORT`, `TM_QA_CHROME` (Chrome binary).
`TM_QA_ORIGIN` screenshots another origin, for example the Vite dev server at
`http://127.0.0.1:5173` while `--keep` runs the API on `TM_QA_PORT=8080`.

Output is written outside the repository. Chrome is required; nothing here runs in CI.
