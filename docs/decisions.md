
---

## ADR-025: Integration tests refuse to run against a non-_test database

**Status:** Accepted

**Context:** `cleanDB` truncates every table. A developer who points
`TEST_DATABASE_URL` at their real database would lose all their data.

**Decision:** `testPool` parses the database name from the connection string
and fails the test unless it ends in `_test`. Integration tests read
`TEST_DATABASE_URL`, not `DATABASE_URL`, so the two cannot be confused.

**Consequences:** A misconfigured test run fails immediately with a clear
message rather than silently destroying data. The cost is that every test
database must be named with a `_test` suffix.
