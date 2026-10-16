package openobserve

import (
	"fmt"
	"testing"
)

// 演示 OpenObserve 数据源“和 OpenObserve 一样的 SQL 查询方式”到底拼出什么 SQL。
func TestOpenObserveSQLMethodDemo(t *testing.T) {
	oo := &OpenObserve{Stream: "default"}
	start, end := int64(1700000000000), int64(1700000060000) // 毫秒

	// 1) 用户在 Explorer 里写的 WHERE 片段（最常见用法）
	sql1 := oo.buildLogSQL("app = 'nginx' AND status = 500", start, end, 100, 0, "default")
	fmt.Println("CASE1 [WHERE片段/日志查询] =>", sql1)

	// 2) 空查询（不带过滤）
	sql2 := oo.buildLogSQL("", start, end, 100, 0, "default")
	fmt.Println("CASE2 [空查询]            =>", sql2)

	// 3) 完整 SELECT（以 select 开头 → 原样透传给 OpenObserve）
	full := `SELECT count(*) AS c FROM "default" WHERE _timestamp BETWEEN 1 AND 2 GROUP BY app`
	sql3 := oo.buildLogSQL(full, start, end, 0, 0, "default")
	fmt.Println("CASE3 [完整SELECT透传]    =>", sql3)

	// 7) 分页：offset=200, limit=100 -> LIMIT 200, 100
	sql7 := oo.buildLogSQL("app = 'nginx'", start, end, 100, 200, "default")
	fmt.Println("CASE7 [分页 offset=200]   =>", sql7)

	// 4) andClause 怎么拼过滤条件
	fmt.Println("CASE4 [andClause]         =>", oo.andClause("level = 'error'"))

	// 5) 日志告警场景：QueryData 把你写的片段包成 count 序列
	q, _ := oo.MakeLogQuery(nil, "level='error'", nil, start, end)
	fmt.Printf("CASE5 [MakeLogQuery产出]   => %+v\n", q)

	// 6) 直方图 SQL
	hq := HistogramQuery{Query: "app='nginx'", Start: start, End: end, Step: "1m"}
	_ = hq
	fmt.Println("说明: QueryHistogram 产出 ->",
		`SELECT count(*) AS value, date_trunc('minute', _timestamp) AS ts FROM "default" WHERE _timestamp BETWEEN 1700000000000000 AND 1700000060000000 AND (app='nginx') GROUP BY ts ORDER BY ts`)
}
