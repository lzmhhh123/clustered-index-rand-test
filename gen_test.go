package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PingCAP-QE/clustered-index-rand-test/sqlgen"
)

func TestGen(t *testing.T) {
	gen := sqlgen.NewGenerator(sqlgen.NewState(func(ctl *sqlgen.ControlOption) {
		ctl.EnableColumnTypeChange = false
		ctl.InitRowCount = 40
		ctl.EnableAggPushDown = true
		ctl.Weight.CreateTable_MustEnumCol = true
		ctl.Weight.Query_INDEX_MERGE = true
		ctl.Weight.Query_DML_DEL = 0
		ctl.Weight.Query_DML_Can_Be_Replace = false
		ctl.Weight.Query_DML_DEL_COMMON = 0
		ctl.Weight.Query_DML_DEL_INDEX = 0
		ctl.Weight.Query_DML_DEL_INDEX_COMMON = 0
		ctl.Weight.Query_DML_DEL_INDEX_PK = 0
		ctl.Weight.Query_DML_INSERT = 0
		ctl.Weight.Query_DML_INSERT_ON_DUP = 0
		ctl.Weight.Query_DML_UPDATE = 0
		ctl.Weight.Query_Window = 0
		ctl.Weight.Query_Union = 0
		ctl.Weight.Query_Prepare = 0
		ctl.Weight.Query_Split = 0
		ctl.Weight.Query_DDL = 0
		ctl.Weight.SetClustered = 0
		ctl.Weight.SetRowFormat = 0
		ctl.Weight.AdminCheck = 0
	}))
	for i := 0; i < 1000; i++ {
		sql := gen()
		sql = strings.Replace(sql, "select /*+   */", "select", -1)
		sql = strings.Replace(sql, "select   ", "select ", -1)
		fmt.Println(sql+";")
	}
}
