package sqlgen

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"github.com/zyguan/sqlz/resultset"
	"log"
	"math/rand"
	"path"
	"strings"
	"time"
)

func NewGenerator(state *State) func() string {
	rand.Seed(time.Now().UnixNano())
	return newGenerator(state)
}

// Separate newGenerator for tests.
func newGenerator(state *State) func() string {
	w := state.ctrl.Weight
	GenPlugins = append(GenPlugins, &ScopeListener{state: state})
	postListener := &PostListener{callbacks: map[string]func(){}}
	GenPlugins = append(GenPlugins, postListener)
	GenPlugins = append(GenPlugins, &DebugListener{})
	if state.ctrl.AttachToTxn {
		GenPlugins = append(GenPlugins, &TxnListener{ctl: state.ctrl})
	}
	retFn := func() string {
		res := evaluateFn(start)
		switch res.Tp {
		case PlainString:
			return res.Value
		case Invalid:
			log.Println("Invalid SQL")
			return ""
		default:
			log.Fatalf("Unsupported result type '%v'", res.Tp)
			return ""
		}
	}

	start = NewFn("start", func() Fn {
		if s, ok := state.PopOneTodoSQL(); ok {
			return Str(s)
		}
		if state.IsInitializing() {
			return initStart
		}
		return Or(
			switchRowFormatVer.SetW(w.SetRowFormat),
			switchClustered.SetW(w.SetClustered),
			adminCheck.SetW(w.AdminCheck),
			If(len(state.tables) < state.ctrl.MaxTableNum,
				Or(
					createTable.SetW(w.CreateTable_WithoutLike),
					createTableLike,
				),
			).SetW(w.CreateTable),
			If(len(state.tables) > 0,
				Or(
					dmlStmt.SetW(w.Query_DML),
					ddlStmt.SetW(w.Query_DDL),
					splitRegion.SetW(w.Query_Split),
					commonAnalyze.SetW(w.Query_Analyze),
					prepareStmt.SetW(w.Query_Prepare),
					If(len(state.prepareStmts) > 0,
						deallocPrepareStmt,
					).SetW(1),
					If(state.ctrl.CanReadGCSavePoint,
						flashBackTable,
					).SetW(1),
					If(state.ctrl.EnableSelectOutFileAndLoadData,
						Or(
							selectIntoOutFile.SetW(1),
							If(!state.Search(ScopeKeyLastOutFileTable).IsNil(),
								loadTable,
							),
						),
					).SetW(1),
				),
			).SetW(w.Query),
		)
	})

	initStart = NewFn("initStart", func() Fn {
		if len(state.tables) < state.ctrl.InitTableCount {
			return createTable
		} else {
			return insertInto
		}
	})

	dmlStmt = NewFn("dmlStmt", func() Fn {
		return Or(
			query.SetW(w.Query_Select),
			If(len(state.prepareStmts) > 0,
				queryPrepare,
			),
			commonDelete.SetW(w.Query_DML_DEL),
			commonInsert.SetW(w.Query_DML_INSERT),
			commonUpdate.SetW(w.Query_DML_UPDATE),
		)
	})

	ddlStmt = NewFn("ddlStmt", func() Fn {
		tbl := state.GetRandTable()
		state.Store(ScopeKeyCurrentTable, NewScopeObj(tbl))
		return Or(
			addColumn,
			addIndex,
			If(len(tbl.Columns) > 1 && tbl.HasDroppableColumn(),
				dropColumn,
			),
			If(len(tbl.Indices) > 0,
				dropIndex,
			),
		)
	})

	switchRowFormatVer = NewFn("switchRowFormat", func() Fn {
		if RandomBool() {
			return Str("set @@global.tidb_row_format_version = 2")
		}
		return Str("set @@global.tidb_row_format_version = 1")
	})

	switchClustered = NewFn("switchClustered", func() Fn {
		if RandomBool() {
			state.enabledClustered = false
			return Str("set @@global.tidb_enable_clustered_index = 0")
		}
		state.enabledClustered = true
		return Str("set @@global.tidb_enable_clustered_index = 1")
	})

	dropTable = NewFn("dropTable", func() Fn {
		tbl := state.GetRandTable()
		state.Store(ScopeKeyLastDropTable, NewScopeObj(tbl))
		return Strs("drop table", tbl.Name)
	})

	flashBackTable = NewFn("flashBackTable", func() Fn {
		tbl := state.GetRandTable()
		state.InjectTodoSQL(fmt.Sprintf("flashback table %s", tbl.Name))
		return Or(
			Strs("drop table", tbl.Name),
			Strs("truncate table", tbl.Name),
		)
	})

	adminCheck = NewFn("adminCheck", func() Fn {
		tbl := state.GetRandTable()
		if state.ctrl.EnableTestTiFlash {
			// Error: Error 1815: Internal : Can't find a proper physical plan for this query
			// https://github.com/pingcap/tidb/issues/22947
			return Str("")
		} else {
			if len(tbl.Indices) == 0 {
				return Strs("admin check table", tbl.Name)
			}
			idx := tbl.GetRandomIndex()
			return Or(
				Strs("admin check table", tbl.Name),
				Strs("admin check index", tbl.Name, idx.Name),
			)
		}
	})

	createTable = NewFn("createTable", func() Fn {
		tbl := GenNewTable(state.AllocGlobalID(ScopeKeyTableUniqID))
		state.AppendTable(tbl)
		postListener.Register("createTable", func() {
			tbl.ReorderColumns()
			tbl.SetPrimaryKeyAndHandle(state)
		})
		colDefs = NewFn("colDefs", func() Fn {
			colDef = NewFn("colDef", func() Fn {
				col := GenNewColumn(state.AllocGlobalID(ScopeKeyColumnUniqID), w)
				tbl.AppendColumn(col)
				return And(Str(col.Name), Str(PrintColumnType(col)))
			})
			if state.IsInitializing() {
				return Repeat(colDef, state.ctrl.InitColCount, Str(","))
			}
			return Or(
				colDef,
				And(colDef, Str(","), colDefs).SetW(w.CreateTable_MoreCol),
			)
		})
		idxDefs = NewFn("idxDefs", func() Fn {
			idxDef = NewFn("idxDef", func() Fn {
				idx := GenNewIndex(state.AllocGlobalID(ScopeKeyIndexUniqID), tbl, w)
				if idx.IsUnique() {
					partitionedCol := state.Search(ScopeKeyCurrentPartitionColumn)
					if !partitionedCol.IsNil() {
						// all partitioned Columns should be contained in every unique/primary index.
						c := partitionedCol.ToColumn()
						Assert(c != nil)
						idx.AppendColumnIfNotExists(c)
					}
				}
				tbl.AppendIndex(idx)
				clusteredKeyword := "/*T![clustered_index] clustered */"
				return And(
					Str(PrintIndexType(idx)),
					Str("key"),
					Str(idx.Name),
					Str("("),
					Str(PrintIndexColumnNames(idx)),
					Str(")"),
					OptIf(idx.Tp == IndexTypePrimary && w.CreateTable_WithClusterHint, Str(clusteredKeyword)),
				)
			})
			return Or(
				idxDef.SetW(1),
				And(idxDef, Str(","), idxDefs).SetW(w.CreateTable_IndexMoreCol),
			)
		})

		partitionDef = NewFn("partitionDef", func() Fn {
			partitionedCol := tbl.GetRandColumnForPartition()
			if partitionedCol == nil {
				return Empty()
			}
			state.StoreInParent(ScopeKeyCurrentPartitionColumn, NewScopeObj(partitionedCol))
			tbl.AppendPartitionColumn(partitionedCol)
			const hashPart, rangePart, listPart = 0, 1, 2
			randN := rand.Intn(6)
			switch w.CreateTable_Partition_Type {
			case "hash":
				randN = hashPart
			case "list":
				randN = listPart
			case "range":
				randN = rangePart
			}
			switch randN {
			case hashPart:
				partitionNum := RandomNum(1, 6)
				return And(
					Str("partition by"),
					Str("hash("),
					Str(partitionedCol.Name),
					Str(")"),
					Str("partitions"),
					Str(partitionNum),
				)
			case rangePart:
				partitionCount := rand.Intn(5) + 1
				vals := partitionedCol.RandomValuesAsc(partitionCount)
				if rand.Intn(2) == 0 {
					partitionCount++
					vals = append(vals, "maxvalue")
				}
				return Strs(
					"partition by range (",
					partitionedCol.Name, ") (",
					PrintRangePartitionDefs(vals),
					")",
				)
			case listPart:
				listVals := partitionedCol.RandomValuesAsc(20)
				listGroups := RandomGroups(listVals, rand.Intn(3)+1)
				return Strs(
					"partition by",
					"list(",
					partitionedCol.Name,
					") (",
					PrintListPartitionDefs(listGroups),
					")",
				)
			default:
				return Empty()
			}
		})
		PreEvalWithOrder(&colDefs, &partitionDef, &idxDefs)
		if state.ctrl.EnableTestTiFlash {
			state.InjectTodoSQL(fmt.Sprintf("alter table %s set tiflash replica 1", tbl.Name))
			state.InjectTodoSQL(fmt.Sprintf("select sleep(20)"))
		}
		indexOpt := 10
		if w.Query_INDEX_MERGE {
			indexOpt = 1000000
		}
		return And(
			Str("create table"),
			Str(tbl.Name),
			Str("("),
			colDefs,
			OptIf(rand.Intn(indexOpt) != 0,
				And(
					Str(","),
					idxDefs,
				),
			),
			Str(")"),
			partitionDef,
		)
	})

	insertInto = NewFn("insertInto", func() Fn {
		tbl := state.GetFirstNonFullTable()
		vals := tbl.GenRandValues(tbl.Columns)
		tbl.AppendRow(vals)
		return And(
			Str("insert into"),
			Str(tbl.Name),
			Str("values"),
			Str("("),
			Str(PrintRandValues(vals)),
			Str(")"),
		)
	})

	query = NewFn("query", func() Fn {
		tbl := state.GetRandTable()
		state.Store(ScopeKeyCurrentTable, NewScopeObj(tbl))
		cols := tbl.GetRandColumns()

		commonSelect = NewFn("commonSelect", func() Fn {
			prepare := state.Search(ScopeKeyCurrentPrepare)
			if !prepare.IsNil() {
				paramCols := SwapOutParameterizedColumns(cols)
				prepare.ToPrepare().AppendColumns(paramCols...)
			}
			return And(Str("select"),
				OptIf(state.ctrl.EnableTestTiFlash,
					And(
						Str("/*+ read_from_storage(tiflash["),
						Str(tbl.Name),
						Str("]) */"),
					)),
				OptIf(w.Query_INDEX_MERGE,
					And(
						Str("/*+ use_index_merge("),
						Str(tbl.Name),
						Str(") */"),
					)),
				Str(PrintColumnNamesWithoutPar(cols, "*")),
				Str("from"),
				Str(tbl.Name),
				Str("where"),
				predicates,
			)
		})
		forUpdateOpt = NewFn("forUpdateOpt", func() Fn {
			return Opt(Str("for update"))
		})
		union = NewFn("union", func() Fn {
			return Or(
				Str("union"),
				Str("union all"),
			)
		})
		aggSelect = NewFn("aggSelect", func() Fn {
			intCol := tbl.GetRandIntColumn()
			if intCol == nil {
				return And(
					Str("select"),
					OptIf(state.ctrl.EnableTestTiFlash,
						And(
							Str("/*+ read_from_storage(tiflash["),
							Str(tbl.Name),
							Str("]) */"),
						)),
					OptIf(w.Query_INDEX_MERGE,
						And(
							Str("/*+ use_index_merge("),
							Str(tbl.Name),
							Str(") */"),
						)),
					Str("count(*) from"),
					Str(tbl.Name),
					Str("where"),
					predicates,
				)
			}
			return Or(
				And(
					Str("select"),
					OptIf(state.ctrl.EnableTestTiFlash,
						And(
							Str("/*+ read_from_storage(tiflash["),
							Str(tbl.Name),
							Str("]) */"),
						)),
					OptIf(w.Query_INDEX_MERGE,
						And(
							Str("/*+ use_index_merge("),
							Str(tbl.Name),
							Str(") */"),
						)),
					Str("count(*) from"),
					Str(tbl.Name),
					Str("where"),
					predicates,
				),
				And(
					Str("select"),
					OptIf(state.ctrl.EnableTestTiFlash,
						And(
							Str("/*+ read_from_storage(tiflash["),
							Str(tbl.Name),
							Str("]) */"),
						)),
					OptIf(w.Query_INDEX_MERGE,
						And(
							Str("/*+ use_index_merge("),
							Str(tbl.Name),
							Str(") */"),
						)),
					Str("sum("),
					Str(intCol.Name),
					Str(")"),
					Str("from"),
					Str(tbl.Name),
					Str("where"),
					predicates,
				),
			)
		})

		return Or(
			And(commonSelect, forUpdateOpt),
			And(
				Str("("), commonSelect, forUpdateOpt, Str(")"),
				union,
				Str("("), commonSelect, forUpdateOpt, Str(")"),
			),
			And(aggSelect, forUpdateOpt),
			And(
				Str("("), aggSelect, forUpdateOpt, Str(")"),
				union,
				Str("("), aggSelect, forUpdateOpt, Str(")"),
			),
			If(len(state.tables) > 1,
				multiTableQuery,
			),
		)
	})

	commonInsert = NewFn("commonInsert", func() Fn {
		tbl := state.GetRandTable()
		var cols []*Column
		if state.ctrl.StrictTransTable {
			cols = tbl.GetRandColumnsIncludedDefaultValue()
		} else {
			cols = tbl.GetRandColumns()
		}
		insertOrReplace := "insert"
		if rand.Intn(3) == 0 && w.Query_DML_Can_Be_Replace {
			insertOrReplace = "replace"
		}

		onDuplicateUpdate = NewFn("onDuplicateUpdate", func() Fn {
			repeatLimit := len(tbl.Columns)

			return Or(
				Empty().SetW(3),
				And(
					Str("on duplicate key update"),
					RepeatRange(1, repeatLimit, onDupAssignment, Str(",")),
				).SetW(w.Query_DML_INSERT_ON_DUP),
			)
		})

		onDupAssignment = NewFn("onDupAssignment", func() Fn {
			randCol := tbl.GetRandColumn()
			return Or(
				Strs(randCol.Name, "=", randCol.RandomValue()),
				Strs(randCol.Name, "=", randCol.Name),
			)
		})

		multipleRowVals = NewFn("multipleRowVals", func() Fn {
			vals := tbl.GenRandValues(cols)
			return Or(
				Strs("(", PrintRandValues(vals), ")").SetW(3),
				And(Strs("(", PrintRandValues(vals), ")"), Str(","), multipleRowVals),
			)
		})

		insertSetStmt = NewFn("insertAssignment", func() Fn {
			randCol := tbl.GetRandColumn()
			return Or(
				Strs(randCol.Name, "=", randCol.RandomValue()),
			)
		})

		// TODO: insert into t partition(p1) values(xxx)
		// TODO: insert ... select... , it's hard to make the selected columns match the inserted columns.
		return Or(
			And(
				Str(insertOrReplace),
				OptIf(insertOrReplace == "insert", Str("ignore")),
				Str("into"),
				Str(tbl.Name),
				Str(PrintColumnNamesWithPar(cols, "")),
				Str("values"),
				multipleRowVals,
				OptIf(insertOrReplace == "insert", onDuplicateUpdate),
			),
			And(
				Str(insertOrReplace),
				OptIf(insertOrReplace == "insert", Str("ignore")),
				Str("into"),
				Str(tbl.Name),
				Str("set"),
				RepeatRange(1, 3, insertSetStmt, Str(",")),
				OptIf(insertOrReplace == "insert", onDuplicateUpdate),
			),
			//And(
			//	Str(insertOrReplace),
			//	Opt(Str("ignore")),
			//	Str("into"),
			//	Str(tbl.Name),
			//	Str(PrintColumnNamesWithPar(cols, "")),
			//	commonSelect,
			//	OptIf(insertOrReplace == "insert", onDuplicateUpdate),
			//),
		)
	})

	commonUpdate = NewFn("commonUpdate", func() Fn {
		tbl := state.GetRandTable()
		state.Store(ScopeKeyCurrentTable, NewScopeObj(tbl))
		orderByCols := tbl.GetRandColumns()

		updateAssignments = NewFn("updateAssignments", func() Fn {
			return Or(
				updateAssignment,
				And(updateAssignment, Str(","), updateAssignments),
			)
		})

		updateAssignment = NewFn("updateAssignment", func() Fn {
			randCol := tbl.GetRandColumn()
			return Or(
				Strs(randCol.Name, "=", randCol.RandomValue()),
			)
		})

		return And(
			Str("update"),
			Str(tbl.Name),
			Str("set"),
			updateAssignments,
			Str("where"),
			predicates,
			OptIf(len(orderByCols) > 0,
				And(
					Str("order by"),
					Str(PrintColumnNamesWithoutPar(orderByCols, "")),
					maybeLimit,
				),
			),
		)
	})

	commonAnalyze = NewFn("commonAnalyze", func() Fn {
		tbl := state.GetRandTable()
		return And(Str("analyze table"), Str(tbl.Name))
	})

	commonDelete = NewFn("commonDelete", func() Fn {
		tbl := state.GetRandTable()
		var col *Column
		if rand.Intn(w.Query_DML_DEL_COMMON+w.Query_DML_DEL_INDEX) < w.Query_DML_DEL_COMMON {
			col = tbl.GetRandColumn()
		} else {
			col = tbl.GetRandIndexFirstColumnWithWeight(w.Query_DML_DEL_INDEX_COMMON, w.Query_DML_DEL_INDEX_COMMON)
		}
		state.Store(ScopeKeyCurrentTable, NewScopeObj(tbl))

		multipleRowVal = NewFn("multipleRowVal", func() Fn {
			return Or(
				Str(col.RandomValue()).SetW(3),
				And(Str(col.RandomValue()), Str(","), multipleRowVal),
			)
		})

		return And(
			Str("delete from"),
			Str(tbl.Name),
			Str("where"),
			Or(
				And(predicates, maybeLimit),
				And(Str(col.Name), Str("in"), Str("("), multipleRowVal, Str(")"), maybeLimit),
				And(Str(col.Name), Str("is null"), maybeLimit),
			),
		)
	})

	predicates = NewFn("predicates", func() Fn {
		if w.Query_INDEX_MERGE {
			andPredicates := NewFn("and predicate", func() Fn {
				var tbl *Table
				inMultiTableQuery := !state.Search(ScopeKeyCurrentMultiTable).IsNil()
				if inMultiTableQuery {
					tables := state.Search(ScopeKeyCurrentMultiTable).ToTables()
					if RandomBool() {
						tbl = tables[0]
					} else {
						tbl = tables[0]
					}
				} else {
					tbl = state.Search(ScopeKeyCurrentTable).ToTable()
				}
				tbl.colForPrefixIndex = tbl.GetRandIndexPrefixColumn()
				repeatCnt := len(tbl.colForPrefixIndex)
				if repeatCnt == 0 {
					repeatCnt = 1
				}
				if rand.Intn(5) == 0 {
					repeatCnt += rand.Intn(2) + 1
				}
				return Repeat(predicate, repeatCnt, Str("and"))
			})

			// Give some chances to common predicate.
			if rand.Intn(5) != 0 {
				return RepeatRange(2, 5, andPredicates, Str("or"))
			}
		}
		return Or(
			predicate.SetW(3),
			And(predicate, Or(Str("and"), Str("or")), predicates),
		)
	})

	predicate = NewFn("predicate", func() Fn {
		var tbl *Table
		inMultiTableQuery := !state.Search(ScopeKeyCurrentMultiTable).IsNil()
		if inMultiTableQuery {
			tables := state.Search(ScopeKeyCurrentMultiTable).ToTables()
			if RandomBool() {
				tbl = tables[0]
			} else {
				tbl = tables[1]
			}
		} else {
			tbl = state.Search(ScopeKeyCurrentTable).ToTable()
		}

		randCol := tbl.GetRandColumn()
		if w.Query_INDEX_MERGE {
			if len(tbl.colForPrefixIndex) > 0 {
				randCol = tbl.colForPrefixIndex[0]
				tbl.colForPrefixIndex = tbl.colForPrefixIndex[1:]
			}
		}
		randVal = NewFn("randVal", func() Fn {
			var v string
			prepare := state.Search(ScopeKeyCurrentPrepare)
			if !prepare.IsNil() && rand.Intn(5) == 0 {
				prepare.ToPrepare().AppendColumns(randCol)
				v = "?"
			} else if rand.Intn(3) == 0 || len(tbl.values) == 0 {
				v = randCol.RandomValue()
			} else {
				v = tbl.GetRandRowVal(randCol)
			}
			return Str(v)
		})
		randColVals = NewFn("randColVals", func() Fn {
			return Or(
				randVal,
				And(randVal, Str(","), randColVals).SetW(3),
			)
		})
		columnName := randCol.Name
		if inMultiTableQuery {
			columnName = fmt.Sprintf("%s.%s", tbl.Name, columnName)
		}
		return Or(
			And(Str(columnName), cmpSymbol, randVal),
			And(Str(columnName), Str("in"), Str("("), randColVals, Str(")")),
		)
	})

	cmpSymbol = NewFn("cmpSymbol", func() Fn {
		return Or(
			Str("="),
			Str("<"),
			Str("<="),
			Str(">"),
			Str(">="),
			Str("<>"),
			Str("!="),
		)
	})

	maybeLimit = NewFn("maybeLimit", func() Fn {
		// Return empty because the limit is not friendly to clustered index AB test.
		return Empty()
	})

	addIndex = NewFn("addIndex", func() Fn {
		tbl := state.Search(ScopeKeyCurrentTable).ToTable()
		idx := GenNewIndex(state.AllocGlobalID(ScopeKeyIndexUniqID), tbl, w)
		tbl.AppendIndex(idx)

		return Strs(
			"alter table", tbl.Name,
			"add index", idx.Name,
			"(", PrintIndexColumnNames(idx), ")",
		)
	})

	dropIndex = NewFn("dropIndex", func() Fn {
		tbl := state.Search(ScopeKeyCurrentTable).ToTable()
		idx := tbl.GetRandomIndex()
		tbl.RemoveIndex(idx)
		return Strs(
			"alter table", tbl.Name,
			"drop index", idx.Name,
		)
	})

	addColumn = NewFn("addColumn", func() Fn {
		tbl := state.Search(ScopeKeyCurrentTable).ToTable()
		col := GenNewColumn(state.AllocGlobalID(ScopeKeyColumnUniqID), w)
		tbl.AppendColumn(col)
		return Strs(
			"alter table", tbl.Name,
			"add column", col.Name, PrintColumnType(col),
		)
	})

	dropColumn = NewFn("dropColumn", func() Fn {
		tbl := state.Search(ScopeKeyCurrentTable).ToTable()
		col := tbl.GetRandDroppableColumn()
		tbl.RemoveColumn(col)
		return Strs(
			"alter table", tbl.Name,
			"drop column", col.Name,
		)
	})

	multiTableQuery = NewFn("multiTableQuery", func() Fn {
		tbl1 := state.GetRandTable()
		tbl2 := state.GetRandTable()
		cols1 := tbl1.GetRandColumns()
		cols2 := tbl2.GetRandColumns()
		state.Store(ScopeKeyCurrentMultiTable, NewScopeObj([]*Table{tbl1, tbl2}))
		preferIndex := RandomBool()
		if preferIndex {
			state.Store(ScopePreferIndexColumn, NewScopeObj(1))
		} else {
			state.Store(ScopePreferIndexColumn, NewScopeObj(0))
		}

		joinPredicates = NewFn("joinPredicates", func() Fn {
			return Or(
				joinPredicate,
				And(joinPredicate, Or(Str("and"), Str("or")), joinPredicates),
			)
		})

		joinPredicate = NewFn("joinPredicate", func() Fn {
			var col1, col2 *Column
			if a := state.Search(ScopePreferIndexColumn); !a.IsNil() && a.ToInt() == 1 {
				col1 = tbl1.GetRandColumnsPreferIndex()
				col2 = tbl2.GetRandColumnsPreferIndex()
			} else {
				col1 = tbl1.GetRandColumnsSimple()
				col2 = tbl2.GetRandColumnsSimple()
			}
			return And(
				Str(col1.Name),
				cmpSymbol,
				Str(col2.Name),
			)
		})
		joinHint = NewFn("joinHint", func() Fn {
			noIndexHint := Or(
				And(
					Str("MERGE_JOIN("),
					Str(tbl1.Name),
					Str(","),
					Str(tbl2.Name),
					Str(")"),
				),
				And(
					Str("HASH_JOIN("),
					Str(tbl1.Name),
					Str(","),
					Str(tbl2.Name),
					Str(")"),
				),
			)

			useIndexHint := Or(
				And(
					Str("INL_JOIN("),
					Str(tbl1.Name),
					Str(","),
					Str(tbl2.Name),
					Str(")"),
				),
				And(
					Str("INL_HASH_JOIN("),
					Str(tbl1.Name),
					Str(","),
					Str(tbl2.Name),
					Str(")"),
				),
				And(
					Str("INL_MERGE_JOIN("),
					Str(tbl1.Name),
					Str(","),
					Str(tbl2.Name),
					Str(")"),
				),
			)
			if preferIndex {
				return Or(
					Empty(),
					useIndexHint,
				)
			}

			return Or(
				Empty(),
				noIndexHint,
			)
		})

		semiJoinStmt = NewFn("seme join", func() Fn {
			var col1, col2 *Column
			preferIndex := !state.Search(ScopePreferIndexColumn).IsNil() && state.Search(ScopePreferIndexColumn).ToInt() == 1
			if preferIndex {
				col1 = tbl1.GetRandColumnsPreferIndex()
				col2 = tbl2.GetRandColumnsPreferIndex()
			} else {
				col1 = tbl1.GetRandColumnsSimple()
				col2 = tbl2.GetRandColumnsSimple()
			}

			// We prefer that the types to be compatible.
			// This method may be extracted to a function later.
			for i := 0; i <= 5; i++ {
				if col1.Tp.IsStringType() && col2.Tp.IsStringType() {
					break
				}
				if col1.Tp.IsIntegerType() && col2.Tp.IsIntegerType() {
					break
				}
				if col1.Tp == col2.Tp {
					break
				}
				if preferIndex {
					col2 = tbl2.GetRandColumnsPreferIndex()
				} else {
					col2 = tbl2.GetRandColumnsSimple()
				}
			}

			// TODO: Support exists subquery.
			return And(
				Str("select"),
				And(
					Str("/*+ "),
					OptIf(state.ctrl.EnableTestTiFlash,
						And(
							Str("read_from_storage(tiflash["),
							Str(tbl1.Name),
							Str(","),
							Str(tbl2.Name),
							Str("])"),
						)),
					joinHint,
					Str(" */"),
				),
				Str(PrintFullQualifiedColName(tbl1, cols1)),
				Str("from"),
				Str(tbl1.Name),
				Str("where"),
				Str(col1.Name),
				Str("in"),
				Str("("),
				Str("select"),
				Str(col2.Name),
				Str("from"),
				Str(tbl2.Name),
				Str("where"),
				predicates,
				Str(")"),
			)
		})

		return Or(
			And(
				Str("select"),
				And(
					Str("/*+ "),
					OptIf(state.ctrl.EnableTestTiFlash,
						And(
							Str("read_from_storage(tiflash["),
							Str(tbl1.Name),
							Str(","),
							Str(tbl2.Name),
							Str("])"),
						)),
					joinHint,
					Str(" */"),
				),
				Str(PrintFullQualifiedColName(tbl1, cols1)),
				Str(","),
				Str(PrintFullQualifiedColName(tbl2, cols2)),
				Str("from"),
				Str(tbl1.Name),
				Or(Str("left join"), Str("join"), Str("right join")),
				Str(tbl2.Name),
				And(Str("on"), joinPredicates),
				And(Str("where")),
				predicates,
			),
			And(
				Str("select"),
				And(
					Str("/*+ "),
					OptIf(state.ctrl.EnableTestTiFlash,
						And(
							Str("read_from_storage(tiflash["),
							Str(tbl1.Name),
							Str(","),
							Str(tbl2.Name),
							Str("])"),
						)),
					OptIf(w.Query_INDEX_MERGE,
						And(
							Str("use_index_merge("),
							Str(tbl1.Name),
							Str(","),
							Str(tbl2.Name),
							Str(")"),
						)),
					joinHint,
					Str(" */"),
				),
				Str(PrintFullQualifiedColName(tbl1, cols1)),
				Str(","),
				Str(PrintFullQualifiedColName(tbl2, cols2)),
				Str("from"),
				Str(tbl1.Name),
				Str("join"),
				Str(tbl2.Name),
			),
			semiJoinStmt,
		)
	})

	createTableLike = NewFn("createTableLike", func() Fn {
		tbl := state.GetRandTable()
		newTbl := tbl.Clone(func() int {
			return state.AllocGlobalID(ScopeKeyTableUniqID)
		}, func() int {
			return state.AllocGlobalID(ScopeKeyColumnUniqID)
		}, func() int {
			return state.AllocGlobalID(ScopeKeyIndexUniqID)
		})
		state.AppendTable(newTbl)
		return Strs("create table", newTbl.Name, "like", tbl.Name)
	})

	selectIntoOutFile = NewFn("selectIntoOutFile", func() Fn {
		tbl := state.GetRandTable()
		state.StoreInRoot(ScopeKeyLastOutFileTable, NewScopeObj(tbl))
		tmpFile := path.Join(SelectOutFileDir, fmt.Sprintf("%s_%d.txt", tbl.Name, state.AllocGlobalID(ScopeKeyTmpFileID)))
		return Strs("select * from", tbl.Name, "into outfile", fmt.Sprintf("'%s'", tmpFile))
	})

	loadTable = NewFn("loadTable", func() Fn {
		tbl := state.Search(ScopeKeyLastOutFileTable).ToTable()
		id := state.Search(ScopeKeyTmpFileID).ToInt()
		tmpFile := path.Join(SelectOutFileDir, fmt.Sprintf("%s_%d.txt", tbl.Name, id))
		randChildTable := tbl.childTables[rand.Intn(len(tbl.childTables))]
		return Strs("load data local infile", fmt.Sprintf("'%s'", tmpFile), "into table", randChildTable.Name)
	})

	splitRegion = NewFn("splitRegion", func() Fn {
		tbl := state.GetRandTable()

		splitRegionBetween = NewFn("splitRegionBetween", func() Fn {
			isSplitTable := state.Search(ScopeKeySplitTableRegion).ToInt() == 1
			var rows [][]string
			var idxOffset int
			if isSplitTable {
				rows = tbl.GenMultipleRowsAscForHandleCols(2)
			} else {
				idxOffset = rand.Intn(len(tbl.Indices))
				rows = tbl.GenMultipleRowsAscForRandomIndexCols(2, idxOffset)
			}
			row1, row2 := rows[0], rows[1]

			return And(
				OptIf(!isSplitTable, Strs("index", tbl.Indices[idxOffset].Name)),
				Strs(
					"between",
					"(", PrintRandValues(row1), ")", "and",
					"(", PrintRandValues(row2), ")", "regions", RandomNum(2, 10)),
			)
		})

		splitRegionBy = NewFn("splitRegionBy", func() Fn {
			isSplitTable := state.Search(ScopeKeySplitTableRegion).ToInt() == 1
			var rows [][]string
			var idxOffset int
			if isSplitTable {
				rows = tbl.GenMultipleRowsAscForHandleCols(rand.Intn(10) + 2)
			} else {
				idxOffset = rand.Intn(len(tbl.Indices))
				rows = tbl.GenMultipleRowsAscForRandomIndexCols(rand.Intn(10)+2, idxOffset)
			}

			byItem := ""
			for _, item := range rows {
				byItem += "("
				byItem += PrintRandValues(item)
				byItem += ")"
				byItem += ","
			}
			byItem = byItem[0 : len(byItem)-1]
			return And(
				OptIf(!isSplitTable, Strs("index", tbl.Indices[idxOffset].Name)),
				Strs(
					"by",
					byItem,
				),
			)
		})

		prefix := Strs("split table", tbl.Name)
		splitRegionTable = NewFn("splitRegionTable", func() Fn {
			state.Store(ScopeKeySplitTableRegion, NewScopeObj(1))
			return Or(And(prefix, splitRegionBetween),
				And(prefix, splitRegionBy))
		})

		splitRegionIndex = NewFn("splitRegionIndex", func() Fn {
			state.Store(ScopeKeySplitTableRegion, NewScopeObj(0))
			return Or(And(prefix, splitRegionBetween),
				And(prefix, splitRegionBy))
		})

		return Or(
			splitRegionTable,
			If(len(tbl.Indices) > 0, splitRegionIndex),
		)
	})

	prepareStmt = NewFn("prepareStmt", func() Fn {
		prepare := GenNewPrepare(state.AllocGlobalID(ScopeKeyPrepareID))
		state.AppendPrepare(prepare)
		state.Store(ScopeKeyCurrentPrepare, NewScopeObj(prepare))
		return And(
			Str("prepare"),
			Str(prepare.Name),
			Str("from"),
			Str(`"`),
			query,
			Str(`"`))
	})

	deallocPrepareStmt = NewFn("deallocPrepareStmt", func() Fn {
		Assert(len(state.prepareStmts) > 0, state)
		prepare := state.GetRandPrepare()
		state.RemovePrepare(prepare)
		return Strs("deallocate prepare", prepare.Name)
	})

	queryPrepare = NewFn("queryPrepare", func() Fn {
		Assert(len(state.prepareStmts) > 0, state)
		prepare := state.GetRandPrepare()
		assignments := prepare.GenAssignments()
		if len(assignments) == 0 {
			return Str(fmt.Sprintf("execute %s", prepare.Name))
		}
		for i := 1; i < len(assignments); i++ {
			state.InjectTodoSQL(assignments[i])
		}
		userVarsStr := strings.Join(prepare.UserVars(), ",")
		state.InjectTodoSQL(fmt.Sprintf("execute %s using %s", prepare.Name, userVarsStr))
		return Str(assignments[0])
	})
	return retFn
}

func RunInteractTest(ctx context.Context, db1, db2 *sql.DB, state *State, sql string) error {
	log.Printf("%s", sql)
	rs1, err1 := runQuery(ctx, db1, sql)
	rs2, err2 := runQuery(ctx, db2, sql)
	if !ValidateErrs(err1, err2) {
		return fmt.Errorf("errors mismatch: %v <> %v %q", err1, err2, sql)
	}
	if rs1 == nil || rs2 == nil {
		return nil
	}
	h1, h2 := rs1.OrderedDigest(resultset.DigestOptions{}), rs2.OrderedDigest(resultset.DigestOptions{})
	if h1 != h2 {
		var b1, b2 bytes.Buffer
		rs1.PrettyPrint(&b1)
		rs2.PrettyPrint(&b2)
		return fmt.Errorf("result digests mismatch: %s != %s %q\n%s\n%s", h1, h2, sql, b1.String(), b2.String())
	}
	if rs1.IsExecResult() && rs1.ExecResult().RowsAffected != rs2.ExecResult().RowsAffected {
		return fmt.Errorf("rows affected mismatch: %d != %d %q",
			rs1.ExecResult().RowsAffected, rs2.ExecResult().RowsAffected, sql)
	}
	return nil
}

func runQuery(ctx context.Context, db *sql.DB, sql string) (*resultset.ResultSet, error) {
	rows, err := db.QueryContext(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return resultset.ReadFromRows(rows)
}

func ValidateErrs(err1 error, err2 error) bool {
	ignoreErrMsgs := []string{
		"with index covered now",                         // 4.0 cannot drop column with index
		"Unknown system variable",                        // 4.0 cannot recognize tidb_enable_clustered_index
		"Split table region lower value count should be", // 4.0 not compatible with 'split table between'
		"for column '_tidb_rowid'",                       // 4.0 split table between may generate incorrect value.
	}
	for _, msg := range ignoreErrMsgs {
		match := OneOfContains(err1, err2, msg)
		if match {
			return true
		}
	}
	return (err1 == nil && err2 == nil) || (err1 != nil && err2 != nil)
}

func OneOfContains(err1, err2 error, msg string) bool {
	c1 := err1 != nil && strings.Contains(err1.Error(), msg) && err2 == nil
	c2 := err2 != nil && strings.Contains(err2.Error(), msg) && err1 == nil
	return c1 || c2
}
