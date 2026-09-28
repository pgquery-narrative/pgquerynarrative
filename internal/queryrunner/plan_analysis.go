package queryrunner

import "fmt"

// detectPlanSignals inspects a single plan node for advanced performance
// signals: filter selectivity, buffer pressure, sort spills, hash batching,
// nested-loop inflation, parallel worker shortage, and partition pruning.
// It complements the seq-scan / high-cost / cardinality checks in walkPlanNode.
func detectPlanSignals(node map[string]interface{}, nodeType, schema, relation string, totalCost float64) []PlanFinding {
	var out []PlanFinding

	// Nodes like Sort or Gather Merge carry no "Relation Name" of their own, but
	// filters/sorts/joins can only be related to existing index definitions if
	// we know which table they apply to. When the node itself lacks a relation
	// and exactly one base table exists in its subtree, attribute the finding
	// to that table so catalog enrichment (index evidence) can still apply.
	effSchema, effRelation := schema, relation
	if effRelation == "" {
		effSchema, effRelation = inferSingleTableRelation(node)
	}
	target := relationOrNode(nodeType, effSchema, effRelation)
	relatedCols := relatedColumnsForNode(node)

	base := func(category, confidence, msg string, evidence []string) PlanFinding {
		return PlanFinding{
			NodeType:       nodeType,
			Schema:         effSchema,
			Relation:       effRelation,
			EstimatedCost:  totalCost,
			Category:       category,
			Confidence:     confidence,
			Message:        msg,
			Evidence:       evidence,
			RelatedColumns: relatedCols,
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
	//
	// EXPLAIN never reports how many sibling partitions the planner pruned at
	// plan time — only executor-time "Subplans Removed", which stays 0 for the
	// common case of a constant, non-sargable predicate decided entirely by the
	// planner before execution starts. So a low child count here can mean either
	// "pruning already worked" (e.g. 3 of 50 partitions, correctly narrowed) or
	// "the table only has a few partitions" — that ambiguity can only be
	// resolved against the catalog's true partition count, which happens during
	// enrichExplainFindings (see explain_catalog.go); this layer only filters
	// out the case it can decide on its own: when no child scan carries a
	// Filter, there is no predicate on the partition key to evaluate at all
	// (an unfiltered scan, e.g. SELECT COUNT(*) FROM t, not a pruning failure).
	if nodeType == "Append" || nodeType == "Merge Append" {
		children, _ := node["Plans"].([]interface{})
		removed, hasRemoved := asFloat64(node["Subplans Removed"])
		leaf := collectAppendLeafInfo(children)
		// Multi-level partitioning represents each top-level partition as its
		// own nested Append, so the true scanned-partition count is the leaf
		// scan count collectAppendLeafInfo found, not len(children) (which
		// would undercount to the number of top-level partitions only).
		scanned := float64(leaf.leafCount)
		if scanned >= 3 && len(leaf.filterColumns) > 0 && (!hasRemoved || removed == 0) {
			confidence := "medium"
			if scanned >= 8 {
				confidence = "high"
			}
			f := base(CategoryPartitionPruning, confidence,
				fmt.Sprintf("%s scans %.0f partitions with no pruning — predicate on the partition key is missing or non-sargable (e.g. DATE_TRUNC on the key)", nodeType, scanned),
				[]string{
					fmt.Sprintf("Subplans Removed=%.0f", removed),
					fmt.Sprintf("child subplans=%.0f", scanned),
				})
			// Append/Merge Append carry no relation of their own — base() left
			// Schema/Relation empty since their children scan different physical
			// partitions (inferSingleTableRelation bails whenever it sees more
			// than one). Attribute this finding to one representative child
			// partition so catalog enrichment can resolve the parent and compare
			// scanned against its true partition count.
			f.Schema = leaf.schema
			f.Relation = leaf.relation
			f.PartitionsScanned = int(scanned)
			f.FilterColumns = leaf.filterColumns
			out = append(out, f)
		}
	}

	return out
}

// appendLeafInfo summarizes what an Append/Merge Append's leaf scans reveal:
// a representative relation to attribute the finding to, and the union of
// columns referenced by every leaf's Filter (used to tell "no predicate on
// the partition key" apart from "a predicate on some other column, which
// still can't be pruned by but isn't a partition-pruning problem").
type appendLeafInfo struct {
	schema        string
	relation      string
	filterColumns []string
	// leafCount is the true number of leaf scans found, including through
	// nested Append/Merge Append descents — the real scanned-partition count
	// for multi-level partitioning, where len(children) at the top level
	// would only count top-level partitions.
	leafCount int
}

// collectAppendLeafInfo walks an Append/Merge Append's children, descending
// into any child that is itself an Append/Merge Append (multi-level
// partitioning presents each top-level partition as a nested Append rather
// than a leaf scan) so a leaf relation and filter columns are still found.
func collectAppendLeafInfo(children []interface{}) appendLeafInfo {
	var info appendLeafInfo
	seenCols := map[string]bool{}
	addCols := func(cols []string) {
		for _, c := range cols {
			if !seenCols[c] {
				seenCols[c] = true
				info.filterColumns = append(info.filterColumns, c)
			}
		}
	}
	var walk func(nodes []interface{})
	walk = func(nodes []interface{}) {
		for _, c := range nodes {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if f, _ := cm["Filter"].(string); f != "" {
				addCols(extractFilterColumns(f))
			}
			if r, _ := cm["Relation Name"].(string); r != "" {
				info.leafCount++
				if info.relation == "" {
					info.relation = r
					info.schema, _ = cm["Schema"].(string)
				}
				continue
			}
			// No relation of its own — a nested Append/Merge Append (or any
			// other intermediate node, e.g. BitmapOr) standing in for a
			// top-level partition. Descend into its own children.
			if nested, ok := cm["Plans"].([]interface{}); ok {
				walk(nested)
			}
		}
	}
	walk(children)
	return info
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
