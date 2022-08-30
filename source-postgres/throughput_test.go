package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/estuary/connectors/sqlcapture"
	"github.com/estuary/connectors/sqlcapture/tests"
	"github.com/estuary/flow/go/protocols/airbyte"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v4"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// The benchmarks 'BenchmarkBackfill' and 'BenchmarkReplication' initialize
// several tables with a bunch of synthetic data and benchmark how long it
// takes to capture the data (via Backfill and Replication, respectively).
//
// In both cases the tests will run one capture, spreading the data across
// 10 tables, with the benchmark 'N' parameter controlling the number of
// rows in each table. To be precise, the number of rows is set to N*100,
// so each 'op' in the 'ns/op' measurement reflects the marginal cost of
// 1,000 row-captures.
//
// These benchmarks have a rather high setup cost, so the default `benchtime`
// target of one second isn't nearly enough. Aim for at least 30 seconds to
// get useful numbers (and 300 seconds for perfect accuracy):
//
//     $ LOG_LEVEL=warn go test -run=NoTests -bench=. -benchtime=30s ./source-postgres/
//     $ LOG_LEVEL=warn go test -run=NoTests -bench=. -benchtime=30s -memprofile memprofile.out -cpuprofile profile.out ./source-postgres/
//
func BenchmarkBackfill(b *testing.B) { benchmarkBackfills(b, 1, 10, b.N*100) }

func BenchmarkReplication(b *testing.B) { benchmarkReplication(b, 1, 10, b.N*100) }

func benchmarkBackfills(b *testing.B, iterations, numTables, rowsPerTable int) {
	b.StopTimer()
	b.ResetTimer()

	// Set up multiple tables full of data
	logrus.WithFields(logrus.Fields{
		"rows":         rowsPerTable * numTables,
		"rowsPerTable": rowsPerTable,
		"tables":       numTables,
	}).Info("initializing tables")

	var tb, ctx = TestBackend, context.Background()
	var tables []string
	var grp errgroup.Group
	for i := 0; i < numTables; i++ {
		var table = tb.CreateTable(ctx, b, fmt.Sprintf("table%d", i), "(id INTEGER PRIMARY KEY, uid TEXT, name TEXT, status INTEGER, modified DATE, foo_id INTEGER, padding TEXT)")
		tables = append(tables, table)
		grp.Go(func() error { return populateTable(ctx, b, tb, table, rowsPerTable) })
	}
	if err := grp.Wait(); err != nil {
		b.Fatalf("error populating tables: %v", err)
	}

	var catalog = tests.ConfiguredCatalog(ctx, b, tb, tables...)
	var dummyOutput = &benchmarkMessageOutput{Inner: json.NewEncoder(io.Discard)}

	logrus.WithField("iterations", iterations).Info("running backfill benchmark")
	for i := 0; i < iterations; i++ {
		// Run a capture of a single empty table. This helps to ensure that the database
		// has fully processed and flushed all of the bulk data loading before we begin
		// timing the actual capture of data we care about.
		var emptyTable = tb.CreateTable(ctx, b, "empty", "(id INTEGER PRIMARY KEY, data TEXT)")
		var dummyCatalog = tests.ConfiguredCatalog(ctx, b, tb, emptyTable)
		var dummyState = sqlcapture.PersistentState{}
		if err := sqlcapture.RunCapture(ctx, tb.GetDatabase(), &dummyCatalog, &dummyState, dummyOutput); err != nil {
			b.Fatalf("capture failed with error: %v", err)
		}
		dummyOutput.Reset()

		var state = sqlcapture.PersistentState{}
		b.StartTimer()
		if err := sqlcapture.RunCapture(ctx, tb.GetDatabase(), &catalog, &state, dummyOutput); err != nil {
			b.Fatalf("capture failed with error: %v", err)
		}
		b.StopTimer()
		var expectedRecords = numTables * rowsPerTable
		if dummyOutput.Total != expectedRecords {
			b.Fatalf("incorrect document count: got %d, expected %d", dummyOutput.Total, expectedRecords)
		}
		dummyOutput.Reset()
	}
}

func benchmarkReplication(b *testing.B, iterations, numTables, rowsPerTable int) {
	b.StopTimer()
	b.ResetTimer()

	var tb, ctx = TestBackend, context.Background()
	var tables []string
	for i := 0; i < numTables; i++ {
		var table = tb.CreateTable(ctx, b, fmt.Sprintf("table%d", i), "(id INTEGER PRIMARY KEY, uid TEXT, name TEXT, status INTEGER, modified DATE, foo_id INTEGER, padding TEXT)")
		tables = append(tables, table)
	}
	var catalog = tests.ConfiguredCatalog(ctx, b, tb, tables...)
	var dummyOutput = &benchmarkMessageOutput{Inner: json.NewEncoder(io.Discard)}

	var initialState = sqlcapture.PersistentState{}
	if err := sqlcapture.RunCapture(ctx, tb.GetDatabase(), &catalog, &initialState, dummyOutput); err != nil {
		b.Fatalf("capture failed with error: %v", err)
	}

	var grp errgroup.Group
	for _, table := range tables {
		var table = table
		grp.Go(func() error { return populateTable(ctx, b, tb, table, rowsPerTable) })
	}
	grp.Wait()

	for i := 0; i < iterations; i++ {
		var state = tests.CopyState(initialState)
		b.StartTimer()
		if err := sqlcapture.RunCapture(ctx, tb.GetDatabase(), &catalog, &state, dummyOutput); err != nil {
			b.Fatalf("capture failed with error: %v", err)
		}
		b.StopTimer()
		var expectedRecords = numTables * rowsPerTable
		if dummyOutput.Total != expectedRecords {
			b.Fatalf("incorrect document count: got %d, expected %d", dummyOutput.Total, expectedRecords)
		}
		dummyOutput.Reset()
	}
}

func populateTable(ctx context.Context, t testing.TB, tb tests.TestBackend, table string, numRows int) error {
	t.Helper()

	const chunkSize = 65536

	var columnNames = []string{"id", "uid", "name", "status", "modified", "foo_id", "padding"}
	var buffer [][]interface{}
	for i := 0; i < numRows; i++ {
		var date = time.Unix(683640000+rand.Int63n(974764800), 0)
		var padding = strings.Repeat("PADDING.", rand.Intn(33))
		buffer = append(buffer, []interface{}{
			// Total size: 192 +/- 132 bytes per row
			i,                           // (4) Serial Integer Primary Key
			uuid.New().String(),         // (36) Random UUID as a string
			fmt.Sprintf("Thing #%d", i), // (8-16) Human readable name
			100 + rand.Intn(400),        // (4) Integer status code
			date,                        // (4) Random YYYY-MM-DD date within the past 30 years
			rand.Int31(),                // (4) Random integer ID
			padding,                     // (0-256) Variable amount of padding
		})
		if len(buffer) >= chunkSize {
			bulkLoadData(ctx, t, table, columnNames, buffer)
			buffer = nil
		}
	}
	if len(buffer) > 0 {
		bulkLoadData(ctx, t, table, columnNames, buffer)
	}
	return nil
}

func bulkLoadData(ctx context.Context, t testing.TB, table string, columnNames []string, rows [][]interface{}) {
	t.Helper()

	if len(rows) < 1 {
		return
	}
	var tx, err = TestBackend.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("unable to begin transaction: %v", err)
	}

	logrus.WithFields(logrus.Fields{"table": table, "count": len(rows)}).Trace("inserting bulk data")
	rowCount, err := tx.CopyFrom(ctx, pgx.Identifier{"public", strings.ToLower(table)}, columnNames, pgx.CopyFromRows(rows))
	if err != nil {
		t.Fatalf("error inserting bulk data: %v", err)
	}
	logrus.WithFields(logrus.Fields{"table": table, "count": rowCount}).Trace("inserted bulk data")

	if rowCount != int64(len(rows)) {
		t.Fatalf("copy inserted too few rows: expected %d, got %d", len(rows), rowCount)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("unable to commit insert transaction: %v", err)
	}
}

type benchmarkMessageOutput struct {
	Total int
	Inner sqlcapture.MessageOutput
}

func (m *benchmarkMessageOutput) Reset() {
	m.Total = 0
}

func (m *benchmarkMessageOutput) Encode(v interface{}) error {
	if msg, ok := v.(airbyte.Message); ok && msg.Type == airbyte.MessageTypeRecord {
		m.Total++
	}
	return m.Inner.Encode(v)
}