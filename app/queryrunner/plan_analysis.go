package queryrunner

import "fmt"

// detectPlanSignals inspects a single plan node for advanced performance
// signals: filter selectivity, buffer pressure, sort spills, hash batching,
// nested-loop inflation, parallel worker shortage, and partition pruning.
// It complements the seq-scan / high-cost / cardinality checks in walkPlanNode.
func detectPlanSignals(node map[string]interface{}, nodeType, schema, relation string, totalCost float64) []PlanFinding {
	var out []PlanFinding
	target := relationOrNode(nodeType, schema, relation)

	base := func(category, confidence, msg string, evidence []string) PlanFinding {
		return PlanFinding{
			NodeType:      nodeType,
			Schema:        schema,
			Relation:      relation,
			EstimatedCost: totalCost,
			Category:      category,
			Confidence:    confidence,
			Message:       msg,
			Evidence:      evidence,
		}
	}

	// Low selectivity: most scanned rows discarded by the filter.
	if removed, ok := asFloat64(node["Rows Removed by Filter"]); ok && removed > 0 {
		returned, _ := asFloat64(node["Actual Rows"])
		scanned := returned + removed
		if scanned >= 1000 {
			discardPct := removed / scanned * 100
			if discardPct >= 90 {
				confidence := "medium"
				if discardPct >= 99 {
					confidence = "high"
				}
				out = append(out, base(CategorySelectivity, confidence,
					fmt.Sprintf("Filter on %s discards %.1f%% of scanned rows (%.0f of %.0f) — an index on the filter columns would avoid most of this work",
						target, discardPct, removed, scanned),
					[]string{
						fmt.Sprintf("Rows Removed by Filter=%.0f", removed),
						fmt.Sprintf("Actual Rows=%.0f", returned),
					}))
			}
		}
	}

	// Buffer pressure: mostly cold reads instead of cache hits (requires BUFFERS).
	if read, ok := asFloat64(node["Shared Read Blocks"]); ok && read >= 1000 {
		hit, _ := asFloat64(node["Shared Hit Blocks"])
		total := read + hit
		if total > 0 && read/total >= 0.5 {
			out = append(out, base(CategoryBufferPressure, "medium",
				fmt.Sprintf("%s on %s read %.0f blocks from disk vs %.0f from cache — working set exceeds shared_buffers or scan touches cold data",
					nodeType, target, read, hit),
				[]string{
					fmt.Sprintf("Shared Read Blocks=%.0f", read),
					fmt.Sprintf("Shared Hit Blocks=%.0f", hit),
				}))
		}
	}

	// Sort spilled to disk.
	if spaceType, _ := node["Sort Space Type"].(string); spaceType == "Disk" {
		spaceUsed, _ := asFloat64(node["Sort Space Used"])
		method, _ := node["Sort Method"].(string)
		out = append(out, base(CategorySortSpill, "high",
			fmt.Sprintf("Sort on %s spilled to disk (~%.0f kB, method %s) — increase work_mem or add an index matching the sort order", target, spaceUsed, method),
			[]string{
				"Sort Space Type=Disk",
				fmt.Sprintf("Sort Space Used=%.0f kB", spaceUsed),
			}))
	}

	// Hash join needed multiple batches (memory pressure).
	if batches, ok := asFloat64(node["Hash Batches"]); ok && batches > 1 {
		peak, _ := asFloat64(node["Peak Memory Usage"])
		out = append(out, base(CategoryHashBatches, "high",
			fmt.Sprintf("Hash on %s used %.0f batches (peak memory %.0f kB) — hash table exceeded work_mem and spilled to disk", target, batches, peak),
			[]string{
				fmt.Sprintf("Hash Batches=%.0f", batches),
				fmt.Sprintf("Peak Memory Usage=%.0f kB", peak),
			}))
	}

	// Nested-loop inflation: inner side executed many times.
	if loops, ok := asFloat64(node["Actual Loops"]); ok && loops >= 1000 {
		if nodeType == "Index Scan" || nodeType == "Index Only Scan" || nodeType == "Seq Scan" || nodeType == "Bitmap Heap Scan" {
			confidence := "medium"
			if loops >= 100000 {
				confidence = "high"
			}
			out = append(out, base(CategoryLoopInflation, confidence,
				fmt.Sprintf("%s on %s executed %.0f times in a nested loop — per-node timings are multiplied by loops; consider a hash/merge join or better join selectivity", nodeType, target, loops),
				[]string{fmt.Sprintf("Actual Loops=%.0f", loops)}))
		}
	}

	// Parallel worker shortage: fewer workers launched than planned.
	if planned, ok := asFloat64(node["Workers Planned"]); ok && planned > 0 {
		if launched, ok := asFloat64(node["Workers Launched"]); ok && launched < planned {
			out = append(out, base(CategoryParallelShortage, "medium",
				fmt.Sprintf("%s planned %.0f parallel workers but launched %.0f — check max_parallel_workers and concurrent load", nodeType, planned, launched),
				[]string{
					fmt.Sprintf("Workers Planned=%.0f", planned),
					fmt.Sprintf("Workers Launched=%.0f", launched),
				}))
		}
	}

	// Partition pruning report on Append/Merge Append nodes.
	if nodeType == "Append" || nodeType == "Merge Append" {
		if removed, ok := asFloat64(node["Subplans Removed"]); ok {
			children, _ := node["Plans"].([]interface{})
			scanned := float64(len(children))
			if removed == 0 && scanned >= 8 {
				out = append(out, base(CategoryPartitionPruning, "medium",
					fmt.Sprintf("%s scans all %.0f partitions with no pruning — add a predicate on the partition key to prune partitions", nodeType, scanned),
					[]string{
						"Subplans Removed=0",
						fmt.Sprintf("child subplans=%.0f", scanned),
					}))
			}
		}
	}

	return out
}

// seqScanEvidence collects the raw plan numbers backing a seq-scan or high-cost finding.
func seqScanEvidence(node map[string]interface{}, filter string) []string {
	var out []string
	if cost, ok := asFloat64(node["Total Cost"]); ok {
		out = append(out, fmt.Sprintf("Total Cost=%.2f", cost))
	}
	if rows, ok := asFloat64(node["Plan Rows"]); ok {
		out = append(out, fmt.Sprintf("Plan Rows=%.0f", rows))
	}
	if filter != "" {
		out = append(out, "Filter="+filter)
	}
	return out
}
