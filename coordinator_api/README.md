# coordinator_api

This is a go web server providing the meat of an API that will validate authenticated users at a basic level, and part of a monorepo example to run in k8s with multiple services/tools.

See [the github repo](https://github.com/catalystcommunity/k8s-monorepo-example) for the rest of the project.

This sub-project should contain tests that use an actual postgres db but the only requirements for testing or running are environment variables.

## Testing

To run tests, you'll need a PostgreSQL database running. You can start one using Docker:

```bash
docker run -d --rm --name postgres-test -e POSTGRES_USER=devuser -e POSTGRES_PASSWORD=devpass -e POSTGRES_DB=testpg -p 5432:5432 postgres:17
```

Then run tests with:
```bash
go test ./test -v
```

The test suite automatically runs migrations and uses transactional rollback for isolation.
