package patentsearch

import (
	"context"
	"sync"
)

type patentQueryBatchResult struct {
	records        []Record
	candidateCount int
	attempts       int
	failures       int
	lastErr        error
}

// searchPatentQueries keeps source/query fan-out bounded while allowing slow
// public portals to progress independently. Results are reassembled in query
// order so ranking and deduplication remain deterministic across runs.
func (client *Client) searchPatentQueries(
	ctx context.Context,
	spec sourceSpec,
	queries []string,
	maxResults int,
	semaphore chan struct{},
) patentQueryBatchResult {
	type queryResult struct {
		index   int
		records []Record
		err     error
	}
	results := make(chan queryResult, len(queries))
	var group sync.WaitGroup
	for index, query := range queries {
		index, query := index, query
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- queryResult{index: index, err: ctx.Err()}
				return
			}
			records, err := client.searchPatentSourceQuery(ctx, spec, query, maxResults)
			results <- queryResult{index: index, records: records, err: err}
		}()
	}
	group.Wait()
	close(results)

	ordered := make([]queryResult, len(queries))
	for result := range results {
		ordered[result.index] = result
	}
	batch := patentQueryBatchResult{attempts: len(queries)}
	seen := map[string]bool{}
	for _, result := range ordered {
		if result.err != nil {
			batch.failures++
			batch.lastErr = result.err
			continue
		}
		batch.candidateCount += len(result.records)
	}
	// Merge query lanes by rank rather than appending one complete language
	// before the next. With a small caller-visible window, sequential merging
	// let the primary-language query occupy every slot and discarded the
	// automatically generated cross-language lane even though it ran
	// successfully. Round-robin preserves the ranking within each query while
	// guaranteeing that distinct bilingual candidates can survive deduplication.
	for rank := 0; ; rank++ {
		addedAtRank := false
		for _, result := range ordered {
			if result.err != nil || rank >= len(result.records) {
				continue
			}
			addedAtRank = true
			record := result.records[rank]
			identity := patentRecordIdentity(record)
			if identity == "" || seen[identity] {
				continue
			}
			seen[identity] = true
			batch.records = append(batch.records, record)
		}
		if !addedAtRank {
			break
		}
	}
	return batch
}
