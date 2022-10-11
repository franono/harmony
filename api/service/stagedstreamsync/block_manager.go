package stagedstreamsync

import (
	"sync"

	"github.com/harmony-one/harmony/core/types"
	sttypes "github.com/harmony-one/harmony/p2p/stream/types"
	"github.com/rs/zerolog"
)

// getBlocksManager is the helper structure for get blocks request management
type getBlocksManager struct {
	chain blockChain

	targetBN   uint64
	requesting map[uint64]struct{} // block numbers that have been assigned to workers but not received
	processing map[uint64]struct{} // block numbers received requests but not inserted
	retries    *prioritizedNumbers // requests where error happens
	rq         *resultQueue        // result queue wait to be inserted into blockchain

	resultC chan struct{}
	logger  zerolog.Logger
	lock    sync.Mutex
}

func newGetBlocksManager(chain blockChain, targetBN uint64, logger zerolog.Logger) *getBlocksManager {
	return &getBlocksManager{
		chain:      chain,
		targetBN:   targetBN,
		requesting: make(map[uint64]struct{}),
		processing: make(map[uint64]struct{}),
		retries:    newPrioritizedNumbers(),
		rq:         newResultQueue(),
		resultC:    make(chan struct{}, 1),
		logger:     logger,
	}
}

// GetNextBatch get the next block numbers batch
func (gbm *getBlocksManager) GetNextBatch() []uint64 {
	gbm.lock.Lock()
	defer gbm.lock.Unlock()

	cap := numBlocksByNumPerRequest

	bns := gbm.getBatchFromRetries(cap)
	cap -= len(bns)
	gbm.addBatchToRequesting(bns)

	if gbm.availableForMoreTasks() {
		addBNs := gbm.getBatchFromUnprocessed(cap)
		gbm.addBatchToRequesting(addBNs)
		bns = append(bns, addBNs...)
	}

	return bns
}

// HandleRequestError handles the error result
func (gbm *getBlocksManager) HandleRequestError(bns []uint64, err error, stid sttypes.StreamID) {
	gbm.lock.Lock()
	defer gbm.lock.Unlock()

	gbm.logger.Warn().Err(err).Str("stream", string(stid)).Msg("get blocks error")

	// add requested block numbers to retries
	for _, bn := range bns {
		delete(gbm.requesting, bn)
		gbm.retries.push(bn)
	}

	// remove results from result queue by the stream and add back to retries
	removed := gbm.rq.removeResultsByStreamID(stid)
	for _, bn := range removed {
		delete(gbm.processing, bn)
		gbm.retries.push(bn)
	}
}

// HandleRequestResult handles get blocks result
func (gbm *getBlocksManager) HandleRequestResult(bns []uint64, blocks []*types.Block, stid sttypes.StreamID) {
	gbm.lock.Lock()
	defer gbm.lock.Unlock()

	for i, bn := range bns {
		delete(gbm.requesting, bn)
		if blocks[i] == nil {
			gbm.retries.push(bn)
		} else {
			gbm.processing[bn] = struct{}{}
		}
	}
	gbm.rq.addBlockResults(blocks, stid)
	select {
	case gbm.resultC <- struct{}{}:
	default:
	}
}

// HandleInsertResult handle the insert result
func (gbm *getBlocksManager) HandleInsertResult(inserted []*blockResult) {
	gbm.lock.Lock()
	defer gbm.lock.Unlock()

	for _, block := range inserted {
		delete(gbm.processing, block.getBlockNumber())
	}
}

// HandleInsertError handles the error during InsertChain
func (gbm *getBlocksManager) HandleInsertError(results []*blockResult, n int) {
	gbm.lock.Lock()
	defer gbm.lock.Unlock()

	var (
		inserted  []*blockResult
		errResult *blockResult
		abandoned []*blockResult
	)
	inserted = results[:n]
	errResult = results[n]
	if n != len(results) {
		abandoned = results[n+1:]
	}

	for _, res := range inserted {
		delete(gbm.processing, res.getBlockNumber())
	}
	for _, res := range abandoned {
		gbm.rq.addBlockResults([]*types.Block{res.block}, res.stid)
	}

	delete(gbm.processing, errResult.getBlockNumber())
	gbm.retries.push(errResult.getBlockNumber())

	removed := gbm.rq.removeResultsByStreamID(errResult.stid)
	for _, bn := range removed {
		delete(gbm.processing, bn)
		gbm.retries.push(bn)
	}
}

// PullContinuousBlocks pull continuous blocks from request queue
func (gbm *getBlocksManager) PullContinuousBlocks(cap int) []*blockResult {
	gbm.lock.Lock()
	defer gbm.lock.Unlock()

	expHeight := gbm.chain.CurrentBlock().NumberU64() + 1
	results, stales := gbm.rq.popBlockResults(expHeight, cap)
	// For stale blocks, we remove them from processing
	for _, bn := range stales {
		delete(gbm.processing, bn)
	}
	return results
}

// getBatchFromRetries get the block number batch to be requested from retries.
func (gbm *getBlocksManager) getBatchFromRetries(cap int) []uint64 {
	var (
		requestBNs []uint64
		curHeight  = gbm.chain.CurrentBlock().NumberU64()
	)
	for cnt := 0; cnt < cap; cnt++ {
		bn := gbm.retries.pop()
		if bn == 0 {
			break // no more retries
		}
		if bn <= curHeight {
			continue
		}
		requestBNs = append(requestBNs, bn)
	}
	return requestBNs
}

// getBatchFromRetries get the block number batch to be requested from unprocessed.
func (gbm *getBlocksManager) getBatchFromUnprocessed(cap int) []uint64 {
	var (
		requestBNs []uint64
		curHeight  = gbm.chain.CurrentBlock().NumberU64()
	)
	bn := curHeight + 1
	// TODO: this algorithm can be potentially optimized.
	for cnt := 0; cnt < cap && bn <= gbm.targetBN; cnt++ {
		for bn <= gbm.targetBN {
			_, ok1 := gbm.requesting[bn]
			_, ok2 := gbm.processing[bn]
			if !ok1 && !ok2 {
				requestBNs = append(requestBNs, bn)
				bn++
				break
			}
			bn++
		}
	}
	return requestBNs
}

func (gbm *getBlocksManager) availableForMoreTasks() bool {
	return gbm.rq.results.Len() < softQueueCap
}

func (gbm *getBlocksManager) addBatchToRequesting(bns []uint64) {
	for _, bn := range bns {
		gbm.requesting[bn] = struct{}{}
	}
}