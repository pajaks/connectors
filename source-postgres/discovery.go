package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/estuary/protocols/airbyte"
	"github.com/jackc/pgx/v4"
	"github.com/sirupsen/logrus"
)

// DiscoverCatalog queries the database and generates an Airbyte Catalog
// describing the available tables and their columns.
func DiscoverCatalog(ctx context.Context, config Config) (*airbyte.Catalog, error) {
	var conn, err = pgx.Connect(ctx, config.ToURI())
	if err != nil {
		return nil, fmt.Errorf("unable to connect to database: %w", err)
	}
	defer conn.Close(ctx)

	tables, err := getDatabaseTables(ctx, conn)
	if err != nil {
		return nil, err
	}

	var catalog = new(airbyte.Catalog)
	for _, table := range tables {
		logrus.WithFields(logrus.Fields{
			"table":      table.Name,
			"namespace":  table.Schema,
			"primaryKey": table.PrimaryKey,
		}).Debug("discovered table")

		var fields = make(map[string]json.RawMessage)
		for _, column := range table.Columns {
			var jsonType, ok = postgresTypeToJSON[column.DataType]
			if !ok {
				return nil, fmt.Errorf("cannot translate PostgreSQL column type %q to JSON schema", column.DataType)
			}

			if jsonType.type_ != "" {
				// If nullable, use `type: [$type_, "null"]` form.
				// Otherwise, use `type: $type_`.
				if column.IsNullable {
					jsonType.RawType, err = json.Marshal([]string{jsonType.type_, "null"})
				} else {
					jsonType.RawType, err = json.Marshal(jsonType.type_)
				}
				if err != nil {
					panic(err)
				}
			}

			// Pass-through a postgres column description.
			if column.Description != nil {
				jsonType.Description = *column.Description
			}

			var out, err = json.Marshal(jsonType)
			if err != nil {
				panic(err)
			}

			fields[column.Name] = out
		}

		var record, err = json.Marshal(map[string]interface{}{
			"type":       "object",
			"required":   table.PrimaryKey,
			"properties": fields,
		})
		if err != nil {
			return nil, fmt.Errorf("error marshalling schema JSON: %w", err)
		}

		// TODO(johnny): |record| is *part* of a schema, but needs to be embedded
		// within a schema describing the overall Debezium wrapper.

		logrus.WithFields(logrus.Fields{
			"table":     table.Name,
			"namespace": table.Schema,
			"columns":   table.Columns,
			"schema":    string(record),
		}).Debug("translated table schema")

		var sourceDefinedPrimaryKey [][]string
		for _, colName := range table.PrimaryKey {
			sourceDefinedPrimaryKey = append(sourceDefinedPrimaryKey, []string{colName})
		}

		catalog.Streams = append(catalog.Streams, airbyte.Stream{
			Name:                    table.Name,
			Namespace:               table.Schema,
			JSONSchema:              json.RawMessage(record),
			SupportedSyncModes:      airbyte.AllSyncModes,
			SourceDefinedCursor:     true,
			SourceDefinedPrimaryKey: sourceDefinedPrimaryKey,
		})
	}
	return catalog, err
}

var postgresTypeToJSON = map[string]struct {
	type_           string
	RawType         json.RawMessage `json:"type,omitempty"`
	ContentEncoding string          `json:"contentEncoding,omitempty"`
	Format          string          `json:"format,omitempty"`
	Description     string          `json:"description,omitempty"`
}{
	"bool": {type_: "boolean"},

	"int2": {type_: "integer"},
	"int4": {type_: "integer"},
	"int8": {type_: "integer"},

	// TODO(wgd): More systematic treatment of arrays?
	"_int2":   {type_: "string"},
	"_int4":   {type_: "string"},
	"_int8":   {type_: "string"},
	"_float4": {type_: "string"},
	"_text":   {type_: "string"},

	"numeric": {type_: "number"},
	"float4":  {type_: "number"},
	"float8":  {type_: "number"},

	"varchar": {type_: "string"},
	"bpchar":  {type_: "string"},
	"text":    {type_: "string"},
	"bytea":   {type_: "string", ContentEncoding: "base64"},
	"xml":     {type_: "string"},
	"bit":     {type_: "string"},
	"varbit":  {type_: "string"},

	"json":     {},
	"jsonb":    {},
	"jsonpath": {type_: "string"},

	// Domain-Specific Types
	"date":        {type_: "string", Format: "date-time"},
	"timestamp":   {type_: "string", Format: "date-time"},
	"timestamptz": {type_: "string", Format: "date-time"},
	"time":        {type_: "integer"},
	"timetz":      {type_: "string", Format: "time"},
	"interval":    {type_: "string"},
	"money":       {type_: "string"},
	"point":       {type_: "string"},
	"line":        {type_: "string"},
	"lseg":        {type_: "string"},
	"box":         {type_: "string"},
	"path":        {type_: "string"},
	"polygon":     {type_: "string"},
	"circle":      {type_: "string"},
	"inet":        {type_: "string"},
	"cidr":        {type_: "string"},
	"macaddr":     {type_: "string"},
	"macaddr8":    {type_: "string"},
	"tsvector":    {type_: "string"},
	"tsquery":     {type_: "string"},
	"uuid":        {type_: "string", Format: "uuid"},
}

// tableInfo represents all relevant knowledge about a PostgreSQL table.
type tableInfo struct {
	Name       string       // The PostgreSQL table name.
	Schema     string       // The PostgreSQL schema (a namespace, in normal parlance) which contains the table.
	Columns    []columnInfo // Information about each column of the table.
	PrimaryKey []string     // An ordered list of the column names which together form the table's primary key.
}

// columnInfo represents a specific column of a specific table in PostgreSQL,
// along with some information about its type.
type columnInfo struct {
	Name        string  // The name of the column.
	Index       int     // The ordinal position of this column in a row.
	TableName   string  // The name of the table to which this column belongs.
	TableSchema string  // The schema of the table to which this column belongs.
	IsNullable  bool    // True if the column can contain nulls.
	DataType    string  // The PostgreSQL type name of this column.
	Description *string // Stored PostgreSQL description of the column, if any.
}

// getDatabaseTables queries the database to produce a list of all tables
// (with the exception of some internal system schemas) with information
// about their column types and primary key.
func getDatabaseTables(ctx context.Context, conn *pgx.Conn) ([]tableInfo, error) {
	// Get lists of all columns and primary keys in the database
	var columns, err = getColumns(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("unable to list database columns: %w", err)
	}
	primaryKeys, err := getPrimaryKeys(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("unable to list database primary keys: %w", err)
	}

	// Aggregate column and primary key information into TableInfo structs
	// using a map from fully-qualified "<schema>.<name>" table names to
	// the corresponding TableInfo.
	var tableMap = make(map[string]*tableInfo)
	for _, column := range columns {
		var id = column.TableSchema + "." + column.TableName
		if _, ok := tableMap[id]; !ok {
			tableMap[id] = &tableInfo{Schema: column.TableSchema, Name: column.TableName}
		}
		tableMap[id].Columns = append(tableMap[id].Columns, column)
	}
	for id, key := range primaryKeys {
		// The `getColumns()` query implements the "exclude system schemas" logic,
		// so here we ignore primary key information for tables we don't care about.
		if _, ok := tableMap[id]; !ok {
			continue
		}
		logrus.WithFields(logrus.Fields{"table": id, "key": key}).Debug("queried primary key")
		tableMap[id].PrimaryKey = key
	}

	// Now that aggregation is complete, discard map keys and return
	// just the list of TableInfo structs.
	var tables []tableInfo
	for _, info := range tableMap {
		tables = append(tables, *info)
	}
	return tables, nil
}

const queryDiscoverColumns = `
  SELECT
		table_schema,
		table_name,
		ordinal_position,
		column_name,
		is_nullable::boolean,
		udt_name,
		pg_catalog.col_description(
			format('%s.%s',table_schema,table_name)::regclass::oid,
			ordinal_position
		) AS column_description
  FROM information_schema.columns
  WHERE
		table_schema != 'pg_catalog' AND
		table_schema != 'information_schema' AND
		table_schema != 'pg_internal' AND
		table_schema != 'catalog_history'
  ORDER BY
		table_schema,
		table_name,
		ordinal_position
	;`

func getColumns(ctx context.Context, conn *pgx.Conn) ([]columnInfo, error) {
	var columns []columnInfo
	var sc columnInfo
	var _, err = conn.QueryFunc(ctx, queryDiscoverColumns, nil,
		[]interface{}{&sc.TableSchema, &sc.TableName, &sc.Index, &sc.Name, &sc.IsNullable, &sc.DataType, &sc.Description},
		func(r pgx.QueryFuncRow) error {
			columns = append(columns, sc)
			return nil
		})
	return columns, err
}

// Query copied from pgjdbc's method PgDatabaseMetaData.getPrimaryKeys() with
// the always-NULL `TABLE_CAT` column omitted.
//
// See: https://github.com/pgjdbc/pgjdbc/blob/master/pgjdbc/src/main/java/org/postgresql/jdbc/PgDatabaseMetaData.java#L2134
const queryDiscoverPrimaryKeys = `
  SELECT result.TABLE_SCHEM, result.TABLE_NAME, result.COLUMN_NAME, result.KEY_SEQ
  FROM (
    SELECT n.nspname AS TABLE_SCHEM,
      ct.relname AS TABLE_NAME, a.attname AS COLUMN_NAME,
      (information_schema._pg_expandarray(i.indkey)).n AS KEY_SEQ, ci.relname AS PK_NAME,
      information_schema._pg_expandarray(i.indkey) AS KEYS, a.attnum AS A_ATTNUM
    FROM pg_catalog.pg_class ct
      JOIN pg_catalog.pg_attribute a ON (ct.oid = a.attrelid)
      JOIN pg_catalog.pg_namespace n ON (ct.relnamespace = n.oid)
      JOIN pg_catalog.pg_index i ON (a.attrelid = i.indrelid)
      JOIN pg_catalog.pg_class ci ON (ci.oid = i.indexrelid)
    WHERE i.indisprimary
  ) result
  WHERE result.A_ATTNUM = (result.KEYS).x
  ORDER BY result.table_name, result.pk_name, result.key_seq;
`

// getPrimaryKeys queries the database to produce a map from table names to
// primary keys. Table names are fully qualified as "<schema>.<name>", and
// primary keys are represented as a list of column names, in the order that
// they form the table's primary key.
func getPrimaryKeys(ctx context.Context, conn *pgx.Conn) (map[string][]string, error) {
	var keys = make(map[string][]string)
	var tableSchema, tableName, columnName string
	var columnIndex int
	var _, err = conn.QueryFunc(ctx, queryDiscoverPrimaryKeys, nil,
		[]interface{}{&tableSchema, &tableName, &columnName, &columnIndex},
		func(r pgx.QueryFuncRow) error {
			var id = fmt.Sprintf("%s.%s", tableSchema, tableName)
			keys[id] = append(keys[id], columnName)
			if columnIndex != len(keys[id]) {
				return fmt.Errorf("primary key column %q appears out of order (expected index %d, in context %q)", columnName, columnIndex, keys[id])
			}
			return nil
		})
	return keys, err
}