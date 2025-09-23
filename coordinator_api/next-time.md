# Coordinator API - Next Session Work Plan

## Current Status Summary

✅ **Completed Work:**
- Comprehensive API testing infrastructure with transaction isolation
- Health endpoints testing (4 test functions, 12+ test cases)
- Jobs API testing (3 test functions, 15+ test cases) 
- Tokens API testing (2 test functions, 8+ test cases)
- Transaction isolation fixes ensuring proper test data cleanup
- UUID validation fixes for proper 404 error responses
- Authentication and authorization testing for user data isolation

## Remaining Tasks (In Priority Order)

### 1. **Authentication Middleware Testing** 🔐
**Status:** In Progress  
**Priority:** Medium  
**Estimated Time:** 2-3 hours

**Scope:**
- Test API token authentication flow
- Test token validation and hashing
- Test user context injection
- Test authentication failure scenarios
- Test middleware chain integration

**Files to Create/Modify:**
- `test/auth_middleware_test.go` (new)
- May need updates to auth middleware for better testability

**Test Cases to Cover:**
- Valid token authentication
- Invalid token rejection
- Missing token handling
- Expired token handling (if implemented)
- Token hash validation
- User context population
- Integration with transaction middleware

### 2. **Store Layer Database Operations Testing** 💾
**Status:** Pending  
**Priority:** Medium  
**Estimated Time:** 4-5 hours

**Scope:**
- Test all database operations in isolation
- Test GORM integration and error handling
- Test transaction handling in store operations
- Test data validation at store level

**Files to Create/Modify:**
- `test/store_operations_test.go` (new)
- May need refactoring of store operations for better testability

**Test Cases to Cover:**
- **User Operations:**
  - CreateUser, GetUser, UpdateUser
  - User role management
  - Duplicate email/username handling
- **Job Operations:**
  - CreateJob, GetJob, UpdateJob, DeleteJob
  - GetJobsByUser with filtering
  - Job status transitions
  - Concurrent job updates
- **Token Operations:**
  - CreateAPIToken, GetAPITokensByUser, DeleteAPIToken
  - Token validation and lookup
  - Token expiration handling
- **Error Scenarios:**
  - Database connection failures
  - Constraint violations
  - Transaction rollback scenarios

### 3. **Worker Command Implementation** ⚙️
**Status:** Pending  
**Priority:** Medium  
**Estimated Time:** 6-8 hours

**Scope:**
- Implement job processing worker
- Add worker command to CLI
- Integrate with job queue system
- Add job status management

**Files to Create/Modify:**
- `cmd/worker.go` (new)
- `internal/worker/` package (new)
- Update `main.go` to include worker command
- May need job queue integration

**Implementation Details:**
- **Worker Command:** `reactorcide worker --queue=default`
- **Job Processing:** Pick up jobs from database, execute via runnerlib
- **Status Updates:** Update job status (running, completed, failed)
- **Error Handling:** Proper logging and error recovery
- **Graceful Shutdown:** Handle SIGTERM/SIGINT properly
- **Concurrency:** Support multiple workers and job concurrency limits

**Integration Points:**
- Use existing runnerlib for job execution
- Update job status in database via store layer
- Log job execution details
- Handle job timeouts and cancellation

### 4. **Single Dockerfile Creation** 🐳
**Status:** Pending  
**Priority:** Medium  
**Estimated Time:** 2-3 hours

**Scope:**
- Create production Dockerfile
- Multi-stage build for optimization
- Default to running server
- Support for different commands

**Files to Create:**
- `Dockerfile` (new)
- `.dockerignore` (new)
- Update deployment documentation

**Dockerfile Requirements:**
- **Base Image:** Use official Go image for building
- **Multi-stage:** Separate build and runtime stages
- **Default Command:** Run the API server
- **Override Support:** Allow running worker or other commands
- **Security:** Non-root user, minimal attack surface
- **Size Optimization:** Small final image size

**Example Usage:**
```bash
# Run API server (default)
docker run reactorcide:latest

# Run worker
docker run reactorcide:latest worker

# Run migrations
docker run reactorcide:latest migrate up
```

## Future Enhancements (Lower Priority)

### 5. **Integration Testing** 🔗
- End-to-end API workflows
- Database migration testing
- Performance testing under load
- Security penetration testing

### 6. **Advanced Features** 🚀
- Job queue management (Redis/PostgreSQL)
- Job scheduling and cron support
- Webhook notifications for job completion
- Job artifact storage and retrieval
- API rate limiting
- Audit logging
- Metrics and monitoring integration

### 7. **Developer Experience** 🛠️
- API documentation generation (OpenAPI/Swagger)
- Development docker-compose setup
- Local development guide
- CI/CD pipeline configuration
- Code coverage reporting

## Technical Debt Items

### Code Quality
- Add more comprehensive error messages
- Implement proper logging throughout the application
- Add input validation middleware
- Standardize API response formats

### Security
- Implement token expiration
- Add API rate limiting
- Audit user permissions and roles
- Add request logging for security monitoring

### Performance
- Add database indexing analysis
- Implement connection pooling optimization
- Add caching for frequently accessed data
- Profile memory usage and optimize

## Testing Strategy Notes

### Current Testing Infrastructure
- ✅ Transaction-based test isolation working perfectly
- ✅ Real database testing with PostgreSQL
- ✅ Authentication testing with real tokens
- ✅ Comprehensive API endpoint coverage

### Testing Patterns Established
- Each sub-test gets its own `RunTransactionalTest()` call
- Users and tokens created fresh for each test
- Real authentication flow using SHA256 token hashing
- DataUtils helper for consistent test data creation
- Proper HTTP status code and response validation

### Reusable Test Components
- `DataUtils` for creating test data (users, jobs, tokens)
- `createAuthTokenHeader()` for authentication
- `GetTestMux()` for consistent router setup
- `RunTransactionalTest()` for database isolation

## Development Environment Notes

### Current Setup
- Go 1.21+ with proper module support
- PostgreSQL database with migrations
- GORM ORM with proper transaction handling
- Testify for assertions and test utilities

### Key Configuration
- `COMMIT_ON_SUCCESS=false` in tests for transaction rollback
- Transaction middleware properly detecting test transactions
- Database migrations working correctly
- Environment variable configuration system

## Next Session Priorities

1. **Start with Authentication Middleware Testing** - builds on current testing infrastructure
2. **Move to Store Layer Testing** - completes the core API testing coverage  
3. **Implement Worker Command** - adds the missing job processing functionality
4. **Finish with Dockerfile** - enables proper deployment

This plan maintains the momentum from the comprehensive API testing work and builds toward a fully functional, well-tested coordinator API system.