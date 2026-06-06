---
name: test
description: Run the plex-photos test suite — Go unit + integration tests, then a browser-driven end-to-end test plan against a live mock-mode dev server. Use when the user types /test or asks to run the tests / test plan / verify everything works.
disable-model-invocation: true
---

# plex-photos /test

Run the full verification suite for this project: automated Go tests first, then
the browser test plan. Report a concise pass/fail summary at the end.

## Workflow

Copy this checklist and track progress:

```
- [ ] Step 1: Run Go unit + integration tests
- [ ] Step 2: Ensure seed images exist
- [ ] Step 3: Start the dev server in mock mode (background)
- [ ] Step 4: Execute the browser test plan
- [ ] Step 5: Stop the server and report results
```

### Step 1: Run Go tests

```
go test ./test/... -count=1 -v
```

If any test fails, stop and report the failures (do not start the browser plan).

### Step 2: Ensure seed images exist

If `testdata/photos` has no images, generate them:

```
go run testdata/gen/gen.go
```

### Step 3: Start the dev server (background, port 8099)

Start it in the background so it does not block. PowerShell:

```
$env:AUTH_PROVIDER="mock"; $env:MOCK_USER="dev"; $env:MOCK_ADMIN="true"; $env:PHOTOS_PATH="./testdata/photos"; $env:DATA_PATH="./testdata/data"; $env:PORT="8099"; go run .
```

Wait until the log shows `listening on :8099`. Note the process PID for Step 5.

### Step 4: Execute the browser test plan

Read [../../test/BROWSER_TEST_PLAN.md](../../test/BROWSER_TEST_PLAN.md) and drive
each test case (TC1–TC9) with the browser tools against `http://localhost:8099`.

- Use the browser-use subagent or the Playwright browser tools.
- For each test case, perform the Action and verify the Expected result.
- On any failure, capture a screenshot and record which TC failed and why.
- TC10 (non-admin) is optional; skip unless the user asks for it.

### Step 5: Stop the server and report

Stop the background dev server (kill its PID). Then report:

```
## Test results

Go tests:        <N passed / M failed>
Browser plan:    <N passed / M failed of TC1–TC9>

<If failures: list each failing test/TC with a one-line reason and link any screenshot.>
```

## Notes

- The dev server writes to `testdata/data` (SQLite + thumbs); this is gitignored
  and safe to delete between runs for a clean slate.
- Mock mode requires no Plex server; auth auto-logs in as the configured user.
- If port 8099 is busy, pick another port and adjust the base URL accordingly.
