package stagedsync

import (
	"context"

	"github.com/harmony-one/harmony/core"
	"github.com/harmony-one/harmony/core/types"
	"github.com/ledgerwatch/erigon-lib/kv"
)

type StageLastMile struct {
	configs StageLastMileCfg
}

type StageLastMileCfg struct {
	ctx context.Context
	bc  *core.BlockChain
	db  kv.RwDB
}

func NewStageLastMile(cfg StageLastMileCfg) *StageLastMile {
	return &StageLastMile{
		configs: cfg,
	}
}

func NewStageLastMileCfg(ctx context.Context, bc *core.BlockChain, db kv.RwDB) StageLastMileCfg {
	return StageLastMileCfg{
		ctx: ctx,
		bc:  bc,
		db:  db,
	}
}

func (lm *StageLastMile) Exec(firstCycle bool, badBlockUnwind bool, s *StageState, unwinder Unwinder, tx kv.RwTx) (err error) {

	bc := lm.configs.bc
	// update blocks after node start sync
	parentHash := bc.CurrentBlock().Hash()
	for {
		block := s.state.getMaxConsensusBlockFromParentHash(parentHash)
		if block == nil {
			break
		}
		err = s.state.UpdateBlockAndStatus(block, bc, true)
		if err != nil {
			break
		}
		parentHash = block.Hash()
	}
	// TODO ek – Do we need to hold syncMux now that syncConfig has its own mutex?
	s.state.syncMux.Lock()
	s.state.syncConfig.ForEachPeer(func(peer *SyncPeerConfig) (brk bool) {
		peer.newBlocks = []*types.Block{}
		return
	})
	s.state.syncMux.Unlock()

	// update last mile blocks if any
	parentHash = bc.CurrentBlock().Hash()
	for {
		block := s.state.getBlockFromLastMileBlocksByParentHash(parentHash)
		if block == nil {
			break
		}
		err = s.state.UpdateBlockAndStatus(block, bc, false)
		if err != nil {
			break
		}
		parentHash = block.Hash()
	}

	return nil
}

func (lm *StageLastMile) Unwind(firstCycle bool, u *UnwindState, s *StageState, tx kv.RwTx) (err error) {
	useExternalTx := tx != nil
	if !useExternalTx {
		tx, err = lm.configs.db.BeginRw(lm.configs.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
	}

	if err = u.Done(tx); err != nil {
		return err
	}
	if !useExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (lm *StageLastMile) Prune(firstCycle bool, p *PruneState, tx kv.RwTx) (err error) {
	useExternalTx := tx != nil
	if !useExternalTx {
		tx, err = lm.configs.db.BeginRw(lm.configs.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
	}

	if !useExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
