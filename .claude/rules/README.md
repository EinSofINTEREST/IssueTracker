# IssueTracker Development Rules

This directory contains the comprehensive ruleset for developing the IssueTracker global issue aggregation system. These rules guide the implementation of a scalable, extensible crawler and data processing pipeline.

## Overview

**IssueTracker** is a system designed to:
- Crawl news, social media, and community sources worldwide
- Process and normalize multilingual content
- Generate embeddings and cluster similar issues
- Track trending topics and cross-country coverage
- Initial focus: United States and South Korea

## Ruleset Structure

The rules are organized into specialized domains:

### [01-architecture.md](01-architecture.md)
**Core System Design and Architecture**

Covers:
- Overall system architecture and layers
- Technology stack requirements
- Directory structure and organization
- Data flow and processing stages
- Scalability considerations
- Multi-environment configuration

Read this first to understand:
- How components fit together
- Design principles and patterns
- Infrastructure requirements
- Storage strategy

### [02-crawler-implementation.md](02-crawler-implementation.md)
**Crawler Development Standards**

Covers:
- Core crawler interface design
- HTTP client configuration
- Country-specific source implementations
- Parsing strategies (HTML, RSS, APIs)
- Anti-bot detection handling
- Rate limiting and job scheduling
- Data models for raw and parsed content

Essential for:
- Implementing new data sources
- Adding support for new countries
- Handling various content formats
- Managing crawl jobs and scheduling

### [03-data-processing.md](03-data-processing.md)
**Data Processing and ML Pipeline**

Covers:
- Processing pipeline stages
- Text normalization and cleaning
- Validation and quality scoring
- Entity extraction and enrichment
- Embedding generation strategies
- Vector storage and indexing
- Clustering algorithms
- Issue detection and tracking

Key for:
- Processing raw crawled data
- Implementing ML features
- Setting up embedding pipeline
- Building issue clustering system

### [04-error-handling.md](04-error-handling.md)
**Error Handling, Monitoring, and Observability**

Covers:
- Error taxonomy and custom error types
- Retry policies and circuit breakers
- Structured logging standards
- Metrics collection with Prometheus
- Health checks and alerting
- Incident response procedures
- Data integrity checks

Critical for:
- Production reliability
- Debugging and troubleshooting
- Performance monitoring
- Operational excellence

### [05-testing.md](05-testing.md)
**Testing Strategy and Quality Assurance**

Covers:
- Unit, integration, and E2E testing
- Mocking strategies
- Table-driven tests
- Benchmarking and profiling
- Code coverage requirements
- Linting and quality gates
- CI/CD workflows
- Load testing

Important for:
- Ensuring code quality
- Preventing regressions
- Performance optimization
- Maintaining test coverage

### [06-code-style.md](06-code-style.md)
**Code Style and Conventions**

Covers:
- Go formatting standards (2-space indentation)
- Naming conventions
- Error handling patterns
- Function and struct design
- Comments and documentation
- Database and configuration styles
- Testing conventions
- Anti-patterns to avoid

Essential for:
- Consistent codebase
- Code readability
- Team collaboration
- Code review efficiency

### [07-workflow.md](07-workflow.md)
**AI Workflow Conventions** (이슈 #152)

Covers:
- Autonomous progression policy — when AI proceeds without user approval
- Exception zones (system changes / destructive perms / external impact / ambiguous scope)
- Commit-per-TODO policy
- PR auto-creation policy with template + closing reference
- Least-privilege permission usage

Essential for:
- AI-assisted development efficiency
- Reducing user-approval friction during routine work
- Safe boundary enforcement on destructive / external operations

## Quick Start Guide

### For New Developers

1. **Start Here**: Read [01-architecture.md](01-architecture.md) for system overview
2. **Code Standards**: Read [06-code-style.md](06-code-style.md) for style guidelines
3. **Set Up Environment**: Follow technology stack requirements
4. **Understand Data Flow**: Review the processing pipeline
5. **Reference Rules**: Use relevant sections while coding

### For Specific Tasks

**Adding a New Data Source:**
1. Review [02-crawler-implementation.md](02-crawler-implementation.md) - Core Crawler Interface
2. Implement crawler following the interface
3. Add tests per [05-testing.md](05-testing.md)
4. Configure error handling per [04-error-handling.md](04-error-handling.md)

**Implementing Processing Features:**
1. Review [03-data-processing.md](03-data-processing.md) - Pipeline Stages
2. Follow normalization and validation rules
3. Implement with proper error handling
4. Add comprehensive tests

**Debugging Production Issues:**
1. Check [04-error-handling.md](04-error-handling.md) - Incident Response
2. Review metrics and logs
3. Follow debugging procedures
4. Update runbooks if needed

**Optimizing Performance:**
1. Review [03-data-processing.md](03-data-processing.md) - Performance Optimization
2. Run benchmarks per [05-testing.md](05-testing.md)
3. Check resource usage in monitoring
4. Implement optimizations incrementally

## Key Principles

### 1. Extensibility First
- Design for adding new countries and sources easily
- Use interface-based architecture
- Plugin pattern for data sources
- Configuration-driven behavior

### 2. Data Quality
- Validate at every stage
- Maintain data lineage
- Implement quality scoring
- Detect and handle duplicates

### 3. Reliability
- Handle failures gracefully
- Implement comprehensive error handling
- Retry with backoff
- Monitor and alert proactively

### 4. Performance
- Design for horizontal scaling
- Use async processing where possible
- Optimize hot paths
- Cache strategically

### 5. Maintainability
- Write clear, documented code
- Follow Go best practices
- Comprehensive testing
- Keep dependencies minimal

## Development Workflow

### Before Writing Code

1. **Understand the Requirement**
   - What problem are we solving?
   - Which component does this affect?
   - Are there existing patterns to follow?

2. **Review Relevant Rules**
   - Check the appropriate ruleset section
   - Understand interfaces and patterns
   - Review error handling requirements
   - Plan test coverage

3. **Design Before Implementation**
   - Sketch data flow
   - Identify dependencies
   - Plan for failure cases
   - Consider performance impact

### While Writing Code

1. **Follow the Style Guide** ([06-code-style.md](06-code-style.md))
   - 2-space indentation
   - Clear, self-documenting names
   - Minimal comments (only WHY)
   - Early returns for errors

2. **Follow the Architecture Rules**
   - Use prescribed interfaces and patterns
   - Implement proper error handling
   - Add structured logging
   - Include context in operations

3. **Write Tests Alongside**
   - Unit tests for logic
   - Integration tests for components
   - Test error paths
   - Add benchmarks for critical paths

4. **Keep it Simple**
   - Remove unnecessary code
   - Avoid premature abstraction
   - Delete commented-out code
   - One responsibility per function

### After Writing Code

1. **Self-Review** (Use [06-code-style.md](06-code-style.md) checklist)
   - Does it follow the style guide?
   - Does it follow the architecture?
   - Are all error cases handled?
   - Is logging sufficient?
   - Are tests comprehensive?
   - No commented-out code?
   - No magic numbers?

2. **Quality Checks**
   - Run linters (golangci-lint)
   - Check test coverage (≥70%)
   - Run benchmarks
   - Verify no new warnings
   - Check code formatting (2-space indent)

3. **Integration Verification**
   - Test with real data (dev environment)
   - Check metrics and logs
   - Verify monitoring/alerting
   - Document any new configurations

## Common Patterns

### Crawler Implementation

```go
// Follow the Crawler interface from 02-crawler-implementation.md
type MyCrawler struct {
    client      HTTPClient
    config      Config
    rateLimiter RateLimiter
}

func (c *MyCrawler) Fetch(ctx context.Context, target Target) (*RawContent, error) {
    // 1. Rate limiting
    if err := c.rateLimiter.Wait(ctx); err != nil {
        return nil, fmt.Errorf("rate limit: %w", err)
    }

    // 2. Build request
    req, err := c.buildRequest(ctx, target)
    if err != nil {
        return nil, &CrawlerError{
            Category: ErrCategoryInternal,
            Code:     "REQ_001",
            Message:  "failed to build request",
            Err:      err,
        }
    }

    // 3. Fetch with retry
    return WithRetry(ctx, NetworkRetryPolicy, func() error {
        return c.doFetch(req)
    })
}
```

### Processing Pipeline

```go
// Follow pipeline pattern from 03-data-processing.md
type Processor struct {
    validator  Validator
    enricher   Enricher
    embedder   Embedder
}

func (p *Processor) Process(ctx context.Context, raw *RawContent) (*ProcessedArticle, error) {
    // 1. Normalize
    normalized, err := p.normalize(raw)
    if err != nil {
        return nil, fmt.Errorf("normalize: %w", err)
    }

    // 2. Validate
    result := p.validator.Validate(normalized)
    if !result.IsValid {
        return nil, &ValidationError{Errors: result.Errors}
    }

    // 3. Enrich
    enriched, err := p.enricher.Enrich(ctx, normalized)
    if err != nil {
        log.Warn().Err(err).Msg("enrichment failed, continuing")
        enriched = normalized
    }

    // 4. Embed
    embedded, err := p.embedder.Embed(ctx, enriched)
    if err != nil {
        return nil, fmt.Errorf("embed: %w", err)
    }

    return embedded, nil
}
```

### Error Handling

```go
// Follow error patterns from 04-error-handling.md
func fetchArticle(ctx context.Context, url string) error {
    resp, err := http.Get(url)
    if err != nil {
        return &CrawlerError{
            Category:  ErrCategoryNetwork,
            Code:      "NET_001",
            Message:   "failed to connect",
            URL:       url,
            Retryable: true,
            Err:       err,
        }
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusTooManyRequests {
        return &CrawlerError{
            Category:   ErrCategoryRateLimit,
            Code:       "HTTP_429",
            Message:    "rate limited",
            URL:        url,
            StatusCode: resp.StatusCode,
            Retryable:  true,
        }
    }

    // ... process response
}
```

## Integration with Claude

When working with Claude for development:

1. **Reference Specific Rules**
   - "Following the crawler interface from 02-crawler-implementation.md..."
   - "As per the error handling rules in 04-error-handling.md..."

2. **Ask for Clarification**
   - "Which pattern from the ruleset should I follow here?"
   - "How does this fit into the architecture from 01-architecture.md?"

3. **Validate Against Rules**
   - "Does this implementation follow the processing pipeline rules?"
   - "Is this error handling consistent with the ruleset?"

## Updates and Evolution

These rules are living documents and should evolve with the project:

### When to Update Rules

- New patterns emerge across the codebase
- Best practices are discovered
- Performance optimizations are proven
- New technologies are adopted
- Production issues reveal gaps

### How to Update

1. Propose changes via discussion
2. Update relevant ruleset file
3. Update this README if structure changes
4. Communicate changes to team
5. Update code to follow new rules

## Additional Resources

### External Documentation

- Go Best Practices: https://go.dev/doc/effective_go
- Prometheus Best Practices: https://prometheus.io/docs/practices/
- PostgreSQL Performance: https://wiki.postgresql.org/wiki/Performance_Optimization

### Internal Documentation

- API Documentation: `docs/api/`
- Deployment Guide: `docs/deployment/`
- Runbooks: `docs/runbooks/`
- Architecture Diagrams: `docs/architecture/`

## Getting Help

- Review the relevant ruleset section first
- Check existing code for examples
- Ask in team chat with specific questions
- Reference the ruleset section in your question

---

**Remember**: These rules exist to ensure consistency, quality, and maintainability. When in doubt, follow the rules. If the rules don't cover a scenario, that's an opportunity to improve them.
