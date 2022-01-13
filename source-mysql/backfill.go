package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/estuary/connectors/sqlcapture"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/sirupsen/logrus"
)

func (db *mysqlDatabase) WriteWatermark(ctx context.Context, watermark string) error {
	logrus.WithField("watermark", watermark).Debug("writing watermark")

	var query = fmt.Sprintf(`REPLACE INTO %s (slot, watermark) VALUES (?,?);`, db.config.WatermarksTable)
	var results, err = db.conn.Execute(query, db.config.ServerID, watermark)
	if err != nil {
		return fmt.Errorf("error upserting new watermark for slot %q: %w", db.config.ServerID, err)
	}
	results.Close()
	return nil
}

func (db *mysqlDatabase) WatermarksTable() string {
	return db.config.WatermarksTable
}

func (db *mysqlDatabase) ScanTableChunk(ctx context.Context, schema, table string, keyColumns []string, resumeKey []interface{}) ([]sqlcapture.ChangeEvent, error) {
	logrus.WithFields(logrus.Fields{
		"schema":     schema,
		"table":      table,
		"keyColumns": keyColumns,
		"resumeKey":  resumeKey,
	}).Debug("scanning table chunk")

	// Build and execute a query to fetch the next `backfillChunkSize` rows from the database
	var query = buildScanQuery(resumeKey == nil, keyColumns, schema, table)
	logrus.WithFields(logrus.Fields{"query": query, "args": resumeKey}).Debug("executing query")
	results, err := db.conn.Execute(query, resumeKey...)
	if err != nil {
		return nil, fmt.Errorf("unable to execute query %q: %w", query, err)
	}
	defer results.Close()

	// Process the results into `changeEvent` structs and return them
	var events []sqlcapture.ChangeEvent
	for _, row := range results.Values {
		var fields = make(map[string]interface{})
		// TODO(wgd): Maybe use 'val.Value()' for this, if we can figure out
		// the []byte vs string decision better elsewhere.
		for idx, val := range row {
			var name = string(results.Fields[idx].Name)
			switch val.Type {
			case mysql.FieldValueTypeUnsigned:
				fields[name] = val.AsUint64()
			case mysql.FieldValueTypeSigned:
				fields[name] = val.AsInt64()
			case mysql.FieldValueTypeFloat:
				fields[name] = val.AsFloat64()
			case mysql.FieldValueTypeString:
				fields[name] = string(val.AsString())
			default: // FieldValueTypeNull
				fields[name] = nil
			}
		}
		logrus.WithField("fields", fields).Trace("got row")
		events = append(events, sqlcapture.ChangeEvent{
			Operation: sqlcapture.InsertOp,
			Source: &mysqlSourceInfo{
				SourceCommon: sqlcapture.SourceCommon{
					Millis:   0, // Not known.
					Schema:   schema,
					Snapshot: true,
					Table:    table,
				},
			},
			Before: nil,
			After:  fields,
		})
	}
	return events, nil
}

// backfillChunkSize controls how many rows will be read from the database in a
// single query. In normal use it acts like a constant, it's just a variable here
// so that it can be lowered in tests to exercise chunking behavior more easily.
var backfillChunkSize = 4096

func buildScanQuery(start bool, keyColumns []string, schemaName, tableName string) string {
	// Construct strings like `(foo, bar, baz)` and `(?, ?, ?)` for use in the query
	var pkey, args string
	for idx, colName := range keyColumns {
		if idx > 0 {
			pkey += ", "
			args += ", "
		}
		pkey += colName
		args += "?"
	}

	// Construct the query itself
	var query = new(strings.Builder)
	fmt.Fprintf(query, "SELECT * FROM %s.%s", schemaName, tableName)
	if !start {
		fmt.Fprintf(query, " WHERE (%s) > (%s)", pkey, args)
	}
	fmt.Fprintf(query, " ORDER BY %s", pkey)
	fmt.Fprintf(query, " LIMIT %d;", backfillChunkSize)
	return query.String()
}