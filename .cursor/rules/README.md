# EcoScrapper Cursor Rules

## Overview

EcoScrapper is a global issue collection and analysis system written in Go, following the standard Go project layout (golang-standards/project-layout).

**Initial Target Markets**: United States and South Korea

## Key Principles

1. **Modularity**: Each data source as separate module
2. **Extensibility**: Easy to add new countries/sources
3. **Data Quality**: Validate at every stage
4. **Reliability**: Handle failures gracefully
5. **Performance**: Design for horizontal scaling

## Documentation Structure

- **[git-conventions.md](git-conventions.md)** - Git commit 및 branch 작성 규칙
- **[code-style.md](code-style.md)** - Go 코드 스타일 가이드
- **[project-structure.md](project-structure.md)** - 프로젝트 구조 및 아키텍처
- **[development-workflow.md](development-workflow.md)** - 개발 워크플로우

## Quick Reference

### Commit Format
```
[{카테고리}]: {변경 내용}
```

### Branch Format
```
{카테고리}/{간단한-설명}
```

### Categories
- **FEAT/feat**: 기능 구현 및 추가
- **FIX/fix**: 버그 수정
- **REFAC/refac**: 구조 변경 및 리팩토링
- **DOCS/docs**: 문서 작업

## External References

- Standard Go Layout: https://github.com/golang-standards/project-layout
- Go Best Practices: https://go.dev/doc/effective_go
- Project Rules: `.claude/rules/`
