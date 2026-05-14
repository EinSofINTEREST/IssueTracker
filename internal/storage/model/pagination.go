package model

// Pagination은 offset 기반 페이지네이션 파라미터를 나타냅니다.
// Limit가 0이면 repository 구현체에서 기본값(50)으로 처리합니다.
type Pagination struct {
	Limit  int
	Offset int
}

// DefaultPagination은 기본 페이지네이션 설정(limit=50, offset=0)을 반환합니다.
func DefaultPagination() Pagination {
	return Pagination{Limit: 50, Offset: 0}
}
