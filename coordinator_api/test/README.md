# Coordinator Database Integration Tests

This package holds the database integration tests of `coordinator_api`. The
tests use a real PostgreSQL database, not mocks.

## How It Works

`TestMain` in `setup_test.go` does these steps one time for the package:

1. It starts a PostgreSQL 17 container with testcontainers-go.
2. It sets `TEST_DB_URI` and `DB_URI` to the connection string of the
   container.
3. It sets `COMMIT_ON_SUCCESS=false`.
4. It runs all migrations (`cmd.RunMigrations`).
5. It opens the test connections (`initTestDB` in `test_utils.go`).
6. It makes the default organization.
7. It runs the tests, then stops the container.

Each test runs in a transaction. The transaction rolls back after the test,
also after a panic. Thus tests do not change the data of other tests, and no
cleanup is necessary.

## Run the Tests

A Docker-compatible container runtime is necessary.

```bash
cd coordinator_api
go test ./test/... -count=1
```

`go test -short` skips this package. `TestMain` exits before it starts the
container.

## Write a Transactional Test

```go
func TestSomething(t *testing.T) {
    RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
        // Use store.AppStore with ctx: the store uses the transaction.
        // Or use tx directly.
        du := &DataUtils{db: tx}
        user, err := du.CreateUser(DataSetup{})
        assert.NoError(t, err)
        _ = user
    })
    // The transaction rolls back here.
}
```

`GetTestMux` in `test_utils.go` returns the application HTTP mux for
handler tests.

## Test Data

`datautils.go` has these methods on `DataUtils`. Each method fills the
fields that `DataSetup` does not give with random values:

- `CreateUser(DataSetup)` makes a user.
- `CreateJob(DataSetup)` makes a job.
- `CreateAPIToken(DataSetup)` makes an API token. When the setup has no
  `UserID`, it makes a user first.
