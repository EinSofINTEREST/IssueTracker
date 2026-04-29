package postgres

// scanner 는 pgx Row / Rows 의 공통 Scan 인터페이스입니다.
// 단일 row 와 다중 row 모두 동일한 헬퍼 함수로 스캔할 수 있도록 추상화합니다.
type scanner interface {
	Scan(dest ...any) error
}
