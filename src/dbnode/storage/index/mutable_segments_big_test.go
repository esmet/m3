// +build big

// Copyright (c) 2021 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package index

import (
	"fmt"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/m3db/m3/src/dbnode/namespace"
	"github.com/m3db/m3/src/m3ninx/doc"
	"github.com/m3db/m3/src/m3ninx/index"
	"github.com/m3db/m3/src/m3ninx/postings"
	"github.com/m3db/m3/src/m3ninx/postings/roaring"
	"github.com/m3db/m3/src/m3ninx/search"
	"github.com/m3db/m3/src/m3ninx/search/query"
	"github.com/m3db/m3/src/x/instrument"
	"github.com/m3db/m3/src/x/pool"
	xsync "github.com/m3db/m3/src/x/sync"
	xtest "github.com/m3db/m3/src/x/test"
	xtime "github.com/m3db/m3/src/x/time"
)

type testMutableSegmentsResult struct {
	logger      *zap.Logger
	cache       *PostingsListCache
	searchCache *PostingsListCache
}

func newTestMutableSegments(
	t *testing.T,
	md namespace.Metadata,
	blockStart time.Time,
) (*mutableSegments, testMutableSegmentsResult) {
	cachedSearchesWorkers := xsync.NewWorkerPool(2)
	cachedSearchesWorkers.Init()

	iOpts := instrument.NewTestOptions(t)

	poolOpts := pool.NewObjectPoolOptions().SetSize(0)
	pool := postings.NewPool(poolOpts, roaring.NewPostingsList)

	cache, _, err := NewPostingsListCache(10, PostingsListCacheOptions{
		PostingsListPool:  pool,
		InstrumentOptions: iOpts,
	})
	require.NoError(t, err)

	searchCache, _, err := NewPostingsListCache(10, PostingsListCacheOptions{
		PostingsListPool:  pool,
		InstrumentOptions: iOpts,
	})
	require.NoError(t, err)

	opts := testOpts.
		SetPostingsListCache(cache).
		SetSearchPostingsListCache(searchCache).
		SetReadThroughSegmentOptions(ReadThroughSegmentOptions{
			CacheRegexp:   true,
			CacheTerms:    true,
			CacheSearches: true,
		})

	segs, err := newMutableSegments(md, blockStart, opts, BlockOptions{},
		cachedSearchesWorkers, namespace.NewRuntimeOptionsManager("foo"), iOpts)
	require.NoError(t, err)

	return segs, testMutableSegmentsResult{
		logger:      iOpts.Logger(),
		searchCache: searchCache,
	}
}

func TestMutableSegmentsBackgroundCompactGCReconstructCachedSearches(t *testing.T) {
	// Use read only postings.
	prevReadOnlyPostings := index.MigrationReadOnlyPostings()
	index.SetMigrationReadOnlyPostings(true)
	defer index.SetMigrationReadOnlyPostings(prevReadOnlyPostings)

	ctrl := xtest.NewController(t)
	defer ctrl.Finish()

	blockSize := time.Hour
	testMD := newTestNSMetadata(t)
	blockStart := time.Now().Truncate(blockSize)

	nowNotBlockStartAligned := blockStart.Add(time.Minute)

	segs, result := newTestMutableSegments(t, testMD, blockStart)
	segs.backgroundCompactDisable = true // Disable to explicitly test.

	inserted := 0
	segs.Lock()
	segsBackground := len(segs.backgroundSegments)
	segs.Unlock()

	for runs := 0; runs < 10; runs++ {
		t.Run(fmt.Sprintf("run-%d", runs), func(t *testing.T) {
			logger := result.logger.With(zap.Int("run", runs))

			// Insert until we have a new background segment.
			for {
				segs.Lock()
				curr := len(segs.backgroundSegments)
				segs.Unlock()
				if curr > segsBackground {
					segsBackground = curr
					break
				}

				batch := NewWriteBatch(WriteBatchOptions{
					IndexBlockSize: blockSize,
				})
				for i := 0; i < 128; i++ {
					stillIndexedBlockStartsAtGC := 1
					if inserted%2 == 0 {
						stillIndexedBlockStartsAtGC = 0
					}
					onIndexSeries := NewMockOnIndexSeries(ctrl)
					onIndexSeries.EXPECT().
						RelookupAndIncrementReaderWriterCount().
						Return(onIndexSeries, true).
						AnyTimes()
					onIndexSeries.EXPECT().
						RemoveIndexedForBlockStarts(gomock.Any()).
						Return(RemoveIndexedForBlockStartsResult{
							IndexedBlockStartsRemaining: stillIndexedBlockStartsAtGC,
						}).
						AnyTimes()
					onIndexSeries.EXPECT().
						DecrementReaderWriterCount().
						AnyTimes()

					batch.Append(WriteBatchEntry{
						Timestamp:     nowNotBlockStartAligned,
						OnIndexSeries: onIndexSeries,
					}, testDocN(inserted))
					inserted++
				}

				_, err := segs.WriteBatch(batch)
				require.NoError(t, err)
			}

			// Perform some searches.
			testDocSearches(t, segs)

			// Make sure search postings cache was populated.
			require.True(t, result.searchCache.lru.Len() > 0)
			logger.Info("search cache populated", zap.Int("n", result.searchCache.lru.Len()))

			// Start some async searches so we have searches going on while
			// executing background compact GC.
			doneCh := make(chan struct{}, 2)
			defer close(doneCh)
			for i := 0; i < 2; i++ {
				go func() {
					for {
						select {
						case <-doneCh:
							return
						default:
						}
						// Search continously.
						testDocSearches(t, segs)
					}
				}()
			}

			// Explicitly background compact and make sure that background segment
			// is GC'd of series no longer present.
			segs.Lock()
			segs.sealedBlockStarts[xtime.ToUnixNano(blockStart)] = struct{}{}
			segs.backgroundCompactGCPending = true
			segs.backgroundCompactWithLock()
			compactingBackgroundStandard := segs.compact.compactingBackgroundStandard
			compactingBackgroundGarbageCollect := segs.compact.compactingBackgroundGarbageCollect
			segs.Unlock()

			// Should have kicked off a background compact GC.
			require.True(t, compactingBackgroundStandard || compactingBackgroundGarbageCollect)

			// Wait for background compact GC to run.
			for {
				segs.Lock()
				compactingBackgroundStandard := segs.compact.compactingBackgroundStandard
				compactingBackgroundGarbageCollect := segs.compact.compactingBackgroundGarbageCollect
				segs.Unlock()
				if !compactingBackgroundStandard && !compactingBackgroundGarbageCollect {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}

			logger.Info("compaction done, search cache", zap.Int("n", result.searchCache.lru.Len()))
		})
	}
}

func testDocSearches(
	t *testing.T,
	segs *mutableSegments,
) {
	for i := 0; i < len(testDocBucket0Values); i++ {
		for j := 0; j < len(testDocBucket1Values); j++ {
			readers, err := segs.AddReaders(nil)
			assert.NoError(t, err)

			regexp0 := fmt.Sprintf("(%s|%s)", moduloByteStr(testDocBucket0Values, i),
				moduloByteStr(testDocBucket0Values, i+1))
			b0, err := query.NewRegexpQuery([]byte(testDocBucket0Name), []byte(regexp0))
			assert.NoError(t, err)

			regexp1 := fmt.Sprintf("(%s|%s|%s)", moduloByteStr(testDocBucket1Values, j),
				moduloByteStr(testDocBucket1Values, j+1),
				moduloByteStr(testDocBucket1Values, j+2))
			b1, err := query.NewRegexpQuery([]byte(testDocBucket1Name), []byte(regexp1))
			assert.NoError(t, err)

			q := query.NewConjunctionQuery([]search.Query{b0, b1})
			searcher, err := q.Searcher()
			assert.NoError(t, err)

			for _, reader := range readers {
				readThrough, ok := reader.(search.ReadThroughSegmentSearcher)
				assert.True(t, ok)

				pl, err := readThrough.Search(q, searcher)
				assert.NoError(t, err)

				assert.True(t, pl.CountSlow() > 0)
			}
		}
	}
}

var (
	testDocBucket0Name   = "bucket_0"
	testDocBucket0Values = []string{
		"one",
		"two",
		"three",
	}
	testDocBucket1Name   = "bucket_1"
	testDocBucket1Values = []string{
		"one",
		"two",
		"three",
		"four",
		"five",
	}
)

func testDocN(n int) doc.Metadata {
	return doc.Metadata{
		ID: []byte(fmt.Sprintf("doc-%d", n)),
		Fields: []doc.Field{
			{
				Name:  []byte("foo"),
				Value: []byte("bar"),
			},
			{
				Name:  []byte(testDocBucket0Name),
				Value: moduloByteStr(testDocBucket0Values, n),
			},
			{
				Name:  []byte(testDocBucket1Name),
				Value: moduloByteStr(testDocBucket1Values, n),
			},
		},
	}
}

func moduloByteStr(strs []string, n int) []byte {
	return []byte(strs[n%len(strs)])
}