// Package main demonstrates using the PgQueryNarrative client as a library:
// create a client from config, run a query, optionally generate a report, then close.
//
// Prerequisites: PostgreSQL running with app and readonly roles, and (for reports) an LLM.
// Set DATABASE_* and LLM_* environment variables.
//
// Build from repo root: go build -o bin/example-library ./examples/library-usage
// Run: ./bin/example-library
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
)

func main() {
	ctx := context.Background()

	// Build config from environment (same as the server).
	cfg := narrative.LoadConfig()

	client, err := narrative.NewClient(ctx, cfg)
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	// Run a simple query.
	sql := "SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category ORDER BY total DESC LIMIT 5"
	result, err := client.RunQuery(ctx, sql, 10)
	if err != nil {
		log.Fatalf("RunQuery: %v", err)
	}

	fmt.Printf("Query returned %d rows in %d ms\n", result.RowCount, result.ExecutionTimeMs)
	for i, col := range result.Columns {
		if i > 0 {
			fmt.Print("\t")
		}
		fmt.Print(col.Name)
	}
	fmt.Println()
	for _, row := range result.Rows {
		for j, v := range row {
			if j > 0 {
				fmt.Print("\t")
			}
			fmt.Printf("%v", v)
		}
		fmt.Println()
	}

	// Optionally generate a report (requires LLM).
	if os.Getenv("LLM_PROVIDER") != "" {
		report, err := client.GenerateReport(ctx, sql)
		if err != nil {
			log.Printf("GenerateReport: %v (skip if LLM not configured)", err)
		} else if report.Narrative != nil {
			fmt.Printf("\nReport headline: %s\n", report.Narrative.Headline)
		}
	}
}
