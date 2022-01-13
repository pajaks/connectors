package main

import (
	"context"
	"fmt"

	"github.com/alecthomas/jsonschema"
	"github.com/estuary/connectors/sqlcapture"
	"github.com/go-mysql-org/go-mysql/client"
	"github.com/sirupsen/logrus"
)

func (db *mysqlDatabase) DiscoverTables(ctx context.Context) (map[string]sqlcapture.TableInfo, error) {
	var columns, err = getColumns(ctx, db.conn)
	if err != nil {
		return nil, fmt.Errorf("error discovering columns: %w", err)
	}
	primaryKeys, err := getPrimaryKeys(ctx, db.conn)
	if err != nil {
		return nil, fmt.Errorf("unable to list database primary keys: %w", err)
	}

	// Aggregate column and primary key information into TableInfo structs
	// using a map from fully-qualified "<schema>.<name>" table names to
	// the corresponding TableInfo.
	var tableMap = make(map[string]sqlcapture.TableInfo)
	for _, column := range columns {
		var id = sqlcapture.JoinStreamID(column.TableSchema, column.TableName)
		var info, ok = tableMap[id]
		if !ok {
			info = sqlcapture.TableInfo{Schema: column.TableSchema, Name: column.TableName}
		}
		info.Columns = append(info.Columns, column)
		tableMap[id] = info
	}
	for id, key := range primaryKeys {
		// The `getColumns()` query implements the "exclude system schemas" logic,
		// so here we ignore primary key information for tables we don't care about.
		var info, ok = tableMap[id]
		if !ok {
			continue
		}
		info.PrimaryKey = key
		tableMap[id] = info
	}
	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		for id, info := range tableMap {
			logrus.WithFields(logrus.Fields{
				"stream":     id,
				"keyColumns": info.PrimaryKey,
			}).Debug("discovered table")
		}
	}
	return tableMap, nil
}

func (db *mysqlDatabase) TranslateDBToJSONType(column sqlcapture.ColumnInfo) (*jsonschema.Type, error) {
	var colSchema, ok = mysqlTypeToJSON[column.DataType]
	if !ok {
		return nil, fmt.Errorf("unhandled MySQL type %q", column.DataType)
	}
	colSchema.nullable = column.IsNullable

	// Pass-through the column description.
	if column.Description != nil {
		colSchema.description = *column.Description
	}
	return colSchema.toType(), nil
}

func (db *mysqlDatabase) TranslateRecordField(val interface{}) (interface{}, error) {
	return val, nil
}

const queryDiscoverColumns = `
  SELECT table_schema, table_name, ordinal_position, column_name, is_nullable, data_type
  FROM information_schema.columns
  WHERE table_schema != 'information_schema' AND table_schema != 'performance_schema'
    AND table_schema != 'mysql' AND table_schema != 'sys'
  ORDER BY table_schema, table_name, ordinal_position;`

func getColumns(ctx context.Context, conn *client.Conn) ([]sqlcapture.ColumnInfo, error) {
	var results, err = conn.Execute(queryDiscoverColumns)
	if err != nil {
		return nil, fmt.Errorf("error querying columns: %w", err)
	}
	defer results.Close()

	var columns []sqlcapture.ColumnInfo
	for _, row := range results.Values {
		columns = append(columns, sqlcapture.ColumnInfo{
			TableSchema: string(row[0].AsString()),
			TableName:   string(row[1].AsString()),
			Index:       int(row[2].AsInt64()),
			Name:        string(row[3].AsString()),
			IsNullable:  string(row[4].AsString()) != "NO",
			DataType:    string(row[5].AsString()),
		})
	}
	return columns, err
}

const queryDiscoverPrimaryKeys = `
SELECT table_schema, table_name, column_name, seq_in_index
  FROM information_schema.statistics
  WHERE index_name = 'primary'
  ORDER BY table_schema, table_name, seq_in_index;
`

// getPrimaryKeys queries the database to produce a map from table names to
// primary keys. Table names are fully qualified as "<schema>.<name>", and
// primary keys are represented as a list of column names, in the order that
// they form the table's primary key.
func getPrimaryKeys(ctx context.Context, conn *client.Conn) (map[string][]string, error) {
	var results, err = conn.Execute(queryDiscoverPrimaryKeys)
	if err != nil {
		return nil, fmt.Errorf("error querying primary keys: %w", err)
	}
	defer results.Close()

	var keys = make(map[string][]string)
	for _, row := range results.Values {
		var streamID = sqlcapture.JoinStreamID(string(row[0].AsString()), string(row[1].AsString()))
		var columnName, index = string(row[2].AsString()), int(row[3].AsInt64())
		logrus.WithFields(logrus.Fields{
			"stream": streamID,
			"column": columnName,
			"index":  index,
		}).Trace("discovered primary-key column")
		keys[streamID] = append(keys[streamID], columnName)
		if index != len(keys[streamID]) {
			return nil, fmt.Errorf("primary key column %q (of table %q) appears out of order", columnName, streamID)
		}
	}
	return keys, nil
}

type columnSchema struct {
	contentEncoding string
	description     string
	format          string
	nullable        bool
	type_           string
}

func (s columnSchema) toType() *jsonschema.Type {
	var out = &jsonschema.Type{
		Format:      s.format,
		Description: s.description,
		Extras:      make(map[string]interface{}),
	}

	if s.contentEncoding != "" {
		out.Extras["contentEncoding"] = s.contentEncoding // New in 2019-09.
	}

	if s.type_ == "" {
		// No type constraint.
	} else if s.nullable {
		out.Extras["type"] = []string{s.type_, "null"} // Use variadic form.
	} else {
		out.Type = s.type_
	}
	return out
}

var mysqlTypeToJSON = map[string]columnSchema{
	"int":     {type_: "integer"},
	"varchar": {type_: "string"},
	"text":    {type_: "string"},
	"double":  {type_: "number"},
}