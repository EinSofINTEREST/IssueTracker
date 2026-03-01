# Development Workflow

## Development Environment Setup

### Prerequisites
- Go 1.22+
- Git
- Make
- golangci-lint (for linting)

### Initial Setup
```bash
# Clone repository
git clone https://github.com/yourusername/issuetracker
cd issuetracker

# Install dependencies
go mod tidy

# Verify setup
make test
```

## Daily Development Workflow

### 1. Start New Feature

#### Create Feature Branch
```bash
# Update main branch
git checkout main
git pull origin main

# Create feature branch
git checkout -b feat/your-feature-name
```

#### Plan Implementation
1. Review relevant rules in `.claude/rules/`
2. Check existing patterns in codebase
3. Plan test coverage
4. Consider error handling

### 2. Implement Feature

#### Write Code
```bash
# Create/modify files
# Follow code style guide (.cursor/code-style.md)
```

#### Write Tests
```bash
# Create test file in test/ directory
# Minimum 70% coverage required
# Use table-driven tests where appropriate
```

#### Example Development Cycle
```bash
# 1. Write failing test
# test/internal_crawler_core/feature_test.go

# 2. Implement feature
# internal/crawler/core/feature.go

# 3. Run tests
make test

# 4. Check coverage
make coverage

# 5. Format code
make fmt

# 6. Run linter
make lint
```

### 3. Quality Checks

#### Before Every Commit
```bash
# 1. Run all tests
make test

# 2. Check coverage (minimum 70%)
make coverage

# 3. Run linter
make lint

# 4. Format code
make fmt

# 5. Verify build
make build
```

#### Pre-Commit Checklist
- [ ] All tests pass
- [ ] Coverage >= 70%
- [ ] No linter errors
- [ ] Code formatted (2-space indentation)
- [ ] No commented-out code
- [ ] No magic numbers
- [ ] Error handling complete
- [ ] Logging includes context

### 4. Commit Changes

#### Stage Changes
```bash
# Review changes
git status
git diff

# Stage files
git add <files>
```

#### Write Commit Message
Follow the format from `.cursor/git-conventions.md`:

```bash
git commit -m "[FEAT]: 기능 설명

- 세부 변경사항 1
- 세부 변경사항 2
- 세부 변경사항 3"
```

**Examples:**
```bash
# Feature
git commit -m "[FEAT]: HTTP client connection pooling 구현

- max idle connections 100으로 설정
- HTTP/2 지원 활성화
- timeout 설정 추가"

# Bug fix
git commit -m "[FIX]: rate limiter deadlock 해결

- mutex lock 순서 변경
- context cancellation 처리 개선"

# Refactoring
git commit -m "[REFAC]: validation 로직 단순화

- 중복 검증 로직 제거
- 함수 복잡도 25줄에서 15줄로 감소"

# Documentation
git commit -m "[DOCS]: development workflow 문서 작성

- .cursor/ 디렉토리에 workflow 가이드 추가
- daily development 절차 정리"
```

### 5. Push and Create PR

#### Push to Remote
```bash
# Push feature branch
git push origin feat/your-feature-name
```

#### Create Pull Request
1. Go to GitHub repository
2. Click "New Pull Request"
3. Select your branch
4. Fill PR template:

```markdown
## Description
[기능 설명]

## Changes
- 변경사항 1
- 변경사항 2

## Testing
- [ ] Unit tests added
- [ ] Integration tests added
- [ ] Manual testing completed

## Checklist
- [ ] Code follows style guide
- [ ] Tests pass (make test)
- [ ] Coverage >= 70%
- [ ] Linter passes (make lint)
- [ ] Documentation updated
```

### 6. Code Review

#### As Author
- Respond to feedback promptly
- Make requested changes
- Push updates to same branch
- Request re-review

#### As Reviewer
- Check code quality
- Verify tests
- Run code locally if needed
- Provide constructive feedback

### 7. Merge and Cleanup

#### After Approval
```bash
# Merge via GitHub PR (Squash and Merge recommended)

# Delete local branch
git checkout main
git pull origin main
git branch -d feat/your-feature-name

# Delete remote branch
git push origin --delete feat/your-feature-name
```

## Makefile Commands

### Build Commands
```bash
make build         # Build crawler binary
make clean         # Clean build artifacts
make deps          # Update dependencies
```

### Test Commands
```bash
make test          # Run all tests
make test-verbose  # Run tests with verbose output
make coverage      # Run tests with coverage
make coverage-html # Generate HTML coverage report
```

### Code Quality Commands
```bash
make fmt           # Format code
make lint          # Run linter
```

### Run Commands
```bash
make run-crawler   # Run crawler
make run-example   # Run example
```

### Help
```bash
make help          # Show all available commands
```

## Testing Strategy

### Unit Tests
```bash
# Run all unit tests
make test

# Run specific package tests
go test ./internal/crawler/core/...

# Run with verbose output
make test-verbose

# Run with coverage
make coverage
```

### Table-Driven Tests
```go
func TestFunction(t *testing.T) {
  tests := []struct {
    name     string
    input    string
    expected string
    wantErr  bool
  }{
    {
      name:     "case 1",
      input:    "input1",
      expected: "output1",
    },
    // More cases...
  }

  for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
      // Test logic
    })
  }
}
```

### Coverage Requirements
- Minimum: **70%**
- Target: **90%+** for core packages
- Critical paths: **100%**

```bash
# Check coverage
make coverage

# View in browser
make coverage-html
```

## Debugging

### Using Logs
```go
import "issuetracker/pkg/logger"

log := logger.New(logger.DefaultConfig())
log.Debug().Str("url", url).Msg("fetching article")
log.Error().Err(err).Msg("failed to fetch")
```

### Using Delve Debugger
```bash
# Install delve
go install github.com/go-delve/delve/cmd/dlv@latest

# Debug test
dlv test ./test/internal_crawler_core -- -test.run TestName

# Debug binary
dlv exec ./bin/crawler
```

### Common Issues

#### Import Errors
```bash
# Verify module path
cat go.mod  # Should show: module issuetracker

# Update dependencies
go mod tidy
```

#### Test Failures
```bash
# Run specific test
go test -v -run TestName ./test/internal_crawler_core

# Check test output
make test-verbose
```

#### Build Errors
```bash
# Clean and rebuild
make clean
make build

# Check Go version
go version  # Should be 1.22+
```

## Release Process

### Version Numbering
Follow Semantic Versioning (SemVer):
- **MAJOR**: Breaking changes
- **MINOR**: New features (backward compatible)
- **PATCH**: Bug fixes

### Release Steps

#### 1. Prepare Release
```bash
# Update version in code
# Update CHANGELOG.md
# Update README.md if needed
```

#### 2. Create Release Branch
```bash
git checkout -b release/v1.2.0
```

#### 3. Run Full Test Suite
```bash
make test
make coverage
make lint
make build
```

#### 4. Create Tag
```bash
git tag -a v1.2.0 -m "Release v1.2.0"
git push origin v1.2.0
```

#### 5. Deploy
```bash
# Deploy to production
# Monitor logs and metrics
```

## Troubleshooting

### Go Module Issues
```bash
# Clear module cache
go clean -modcache

# Re-download dependencies
rm go.sum
go mod tidy
```

### Test Issues
```bash
# Clear test cache
go clean -testcache

# Run tests fresh
make test
```

### Build Issues
```bash
# Clean artifacts
make clean

# Rebuild
make build
```

## Best Practices

### 1. Commit Often
- Small, focused commits
- One logical change per commit
- Clear commit messages

### 2. Test First
- Write tests before code (TDD)
- Ensure tests fail first
- Then implement feature

### 3. Code Review
- Review your own code first
- Check all quality requirements
- Be open to feedback

### 4. Documentation
- Update docs with code changes
- Add examples for new features
- Keep README current

### 5. Continuous Integration
- All tests must pass
- Linter must pass
- Coverage requirements met

## Getting Help

### Resources
- Project rules: `.claude/rules/`
- Cursor rules: `.cursor/`
- Go documentation: https://go.dev/doc/
- Standard layout: https://github.com/golang-standards/project-layout

### Team Communication
- Create GitHub issues for bugs
- Discuss features in PRs
- Ask questions in team chat

## Quick Reference

### Common Commands
```bash
# Start new feature
git checkout -b feat/feature-name

# Development cycle
make test && make lint && make fmt

# Commit changes
git commit -m "[FEAT]: 기능 설명"

# Push and create PR
git push origin feat/feature-name

# After merge
git checkout main && git pull
git branch -d feat/feature-name
```

### File Locations
- Source code: `internal/`, `pkg/`
- Tests: `test/`
- Examples: `examples/`
- Rules: `.claude/rules/`, `.cursor/`
- Build: `Makefile`
