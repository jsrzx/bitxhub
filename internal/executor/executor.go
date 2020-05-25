package executor

import (
	"context"
	"fmt"

	"github.com/meshplus/bitxhub/internal/executor/contracts"

	"github.com/ethereum/go-ethereum/event"
	"github.com/meshplus/bitxhub-core/validator"
	"github.com/meshplus/bitxhub-kit/cache"
	"github.com/meshplus/bitxhub-kit/types"
	"github.com/meshplus/bitxhub-model/pb"
	"github.com/meshplus/bitxhub/internal/constant"
	"github.com/meshplus/bitxhub/internal/ledger"
	"github.com/meshplus/bitxhub/internal/model/events"
	"github.com/meshplus/bitxhub/pkg/vm/boltvm"
	"github.com/sirupsen/logrus"
	"github.com/wasmerio/go-ext-wasm/wasmer"
)

const (
	blockChanNumber   = 1024
	persistChanNumber = 1024
)

var _ Executor = (*BlockExecutor)(nil)

// BlockExecutor executes block from order
type BlockExecutor struct {
	ledger            ledger.Ledger
	logger            logrus.FieldLogger
	blockC            chan *pb.Block
	persistC          chan *ledger.BlockData
	pendingBlockQ     *cache.Cache
	interchainCounter map[string][]uint64
	normalTxs         []types.Hash
	validationEngine  validator.Engine
	currentHeight     uint64
	currentBlockHash  types.Hash
	boltContracts     map[string]boltvm.Contract
	wasmInstances     map[string]wasmer.Instance

	blockFeed event.Feed

	ctx    context.Context
	cancel context.CancelFunc
}

// New creates executor instance
func New(chainLedger ledger.Ledger, logger logrus.FieldLogger) (*BlockExecutor, error) {
	pendingBlockQ, err := cache.NewCache()
	if err != nil {
		return nil, fmt.Errorf("create cache: %w", err)
	}

	ve := validator.NewValidationEngine(chainLedger, logger)

	boltContracts := registerBoltContracts()

	ctx, cancel := context.WithCancel(context.Background())

	return &BlockExecutor{
		ledger:            chainLedger,
		logger:            logger,
		interchainCounter: make(map[string][]uint64),
		ctx:               ctx,
		cancel:            cancel,
		blockC:            make(chan *pb.Block, blockChanNumber),
		persistC:          make(chan *ledger.BlockData, persistChanNumber),
		pendingBlockQ:     pendingBlockQ,
		validationEngine:  ve,
		currentHeight:     chainLedger.GetChainMeta().Height,
		currentBlockHash:  chainLedger.GetChainMeta().BlockHash,
		boltContracts:     boltContracts,
		wasmInstances:     make(map[string]wasmer.Instance),
	}, nil
}

// Start starts executor
func (exec *BlockExecutor) Start() error {
	go exec.listenExecuteEvent()

	go exec.persistData()

	exec.logger.WithFields(logrus.Fields{
		"height": exec.currentHeight,
		"hash":   exec.currentBlockHash.ShortString(),
	}).Infof("Executor started")

	return nil
}

// Stop stops executor
func (exec *BlockExecutor) Stop() error {
	exec.cancel()

	exec.logger.Info("Executor stopped")

	return nil
}

// ExecuteBlock executes block from order
func (exec *BlockExecutor) ExecuteBlock(block *pb.Block) {
	exec.blockC <- block
}

func (exec *BlockExecutor) SyncExecuteBlock(block *pb.Block) {
	exec.handleExecuteEvent(block)
}

// SubscribeBlockEvent registers a subscription of NewBlockEvent.
func (exec *BlockExecutor) SubscribeBlockEvent(ch chan<- events.NewBlockEvent) event.Subscription {
	return exec.blockFeed.Subscribe(ch)
}

func (exec *BlockExecutor) listenExecuteEvent() {
	for {
		select {
		case block := <-exec.blockC:
			exec.handleExecuteEvent(block)
		case <-exec.ctx.Done():
			close(exec.persistC)
			return
		}
	}
}

func (exec *BlockExecutor) persistData() {
	for data := range exec.persistC {
		exec.ledger.PersistBlockData(data)
	}
}

func registerBoltContracts() map[string]boltvm.Contract {
	boltContracts := []*boltvm.BoltContract{
		{
			Enabled:  true,
			Name:     "interchain manager contract",
			Address:  constant.InterchainContractAddr.String(),
			Contract: &contracts.InterchainManager{},
		},
		{
			Enabled:  true,
			Name:     "store service",
			Address:  constant.StoreContractAddr.String(),
			Contract: &contracts.Store{},
		},
		{
			Enabled:  true,
			Name:     "rule manager service",
			Address:  constant.RuleManagerContractAddr.String(),
			Contract: &contracts.RuleManager{},
		},
		{
			Enabled:  true,
			Name:     "role manager service",
			Address:  constant.RoleContractAddr.String(),
			Contract: &contracts.Role{},
		},
		{
			Enabled:  true,
			Name:     "appchain manager service",
			Address:  constant.AppchainMgrContractAddr.String(),
			Contract: &contracts.AppchainManager{},
		},
	}

	return boltvm.Register(boltContracts)
}
