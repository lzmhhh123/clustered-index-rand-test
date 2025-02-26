package sqlgen

type ControlOption struct {
	// initial value for state.enableClustered
	InitEnableClustered bool
	// the initial number of tables.
	InitTableCount int
	// the number of rows to initialize for each table.
	InitRowCount int
	// the number of Columns for each tables.
	InitColCount int

	// the max number of tables.
	MaxTableNum int
	// for the Columns that have no default value,
	// whether allow to omit column names in 'INSERT' statement.
	StrictTransTable bool
	// indicate that the testing server has gc save point.
	CanReadGCSavePoint bool
	// Test SELECT OUTFILE and LOAD DATA
	EnableSelectOutFileAndLoadData bool
	// Test TiFlash
	EnableTestTiFlash bool
	// indicate whether attach stmt inside txn
	AttachToTxn bool
	// max stmt count in a txn
	MaxTxnStmtCount int
	// generate SQL weight
	Weight *Weight
}

type Weight struct {
	CreateTable                 int
	CreateTable_WithClusterHint bool
	CreateTable_MoreCol         int
	CreateTable_WithoutLike     int
	CreateTable_Partition_Type  string
	CreateTable_IndexMoreCol    int
	CreateTable_MustPrefixIndex bool
	CreateTable_MustStrCol      bool
	CreateTable_MustIntCol      bool
	Query                       int
	Query_DML                   int
	Query_Select                int
	Query_DML_DEL               int
	Query_DML_DEL_INDEX         int
	Query_DML_DEL_COMMON        int
	Query_DML_DEL_INDEX_PK      int
	Query_DML_DEL_INDEX_COMMON  int
	Query_DML_INSERT            int
	Query_DML_INSERT_ON_DUP     int
	Query_DML_Can_Be_Replace    bool
	Query_DML_UPDATE            int
	Query_DDL                   int
	Query_Split                 int
	Query_Analyze               int
	Query_Prepare               int
	Query_HasLimit              int
	Query_INDEX_MERGE           bool
	SetRowFormat                int
	SetClustered                int
	AdminCheck                  int
}

func DefaultControlOption() *ControlOption {
	cloneWeight := DefaultWeight
	return &ControlOption{
		InitEnableClustered:            true,
		InitTableCount:                 5,
		InitRowCount:                   10,
		InitColCount:                   5,
		MaxTableNum:                    7,
		StrictTransTable:               true,
		CanReadGCSavePoint:             false,
		EnableSelectOutFileAndLoadData: false,
		EnableTestTiFlash:              false,
		AttachToTxn:                    false,
		MaxTxnStmtCount:                20,
		Weight:                         &cloneWeight,
	}
}

var DefaultWeight = Weight{
	CreateTable:                 13,
	CreateTable_WithClusterHint: true,
	CreateTable_MoreCol:         2,
	CreateTable_IndexMoreCol:    2,
	CreateTable_MustPrefixIndex: false,
	CreateTable_WithoutLike:     4,
	CreateTable_Partition_Type:  "",
	CreateTable_MustStrCol:      false,
	CreateTable_MustIntCol:      false,
	Query:                       15,
	Query_DML:                   20,
	Query_Select:                1,
	Query_DML_DEL:               1,
	Query_DML_DEL_INDEX:         0,
	Query_DML_DEL_COMMON:        1,
	Query_DML_DEL_INDEX_PK:      1,
	Query_DML_DEL_INDEX_COMMON:  1,
	Query_DML_UPDATE:            1,
	Query_DML_INSERT:            1,
	Query_DML_INSERT_ON_DUP:     4,
	Query_DML_Can_Be_Replace:    true,
	Query_DDL:                   5,
	Query_Split:                 0,
	Query_Analyze:               0,
	Query_Prepare:               2,
	Query_HasLimit:              1,
	SetClustered:                1,
	SetRowFormat:                1,
	AdminCheck:                  1,
}
