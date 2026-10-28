package store

import "context"

// QueryInt 跑单值聚合查询，SQL 由调用方给出。
// 测试与 CLI 都需要「数一下 / 取一个标量」，与其为每种统计加一个专用方法，
// 不如开放一个通用原语——生产代码里不会留下没人调用的死方法。
func (s *Store) QueryInt(ctx context.Context, q string, args ...any) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// QueryStrings 跑多行单列查询。
func (s *Store) QueryStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
