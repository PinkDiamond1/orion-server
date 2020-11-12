package backend

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.ibm.com/blockchaindb/library/pkg/logger"
	"github.ibm.com/blockchaindb/server/pkg/blockcreator"
	"github.ibm.com/blockchaindb/server/pkg/blockprocessor"
	"github.ibm.com/blockchaindb/server/pkg/blockstore"
	"github.ibm.com/blockchaindb/server/pkg/queue"
	"github.ibm.com/blockchaindb/server/pkg/txreorderer"
	"github.ibm.com/blockchaindb/server/pkg/worldstate"
)

type transactionProcessor struct {
	txQueue             *queue.Queue
	txBatchQueue        *queue.Queue
	blockQueue          *queue.Queue
	txReorderer         *txreorderer.TxReorderer
	blockCreator        *blockcreator.BlockCreator
	blockProcessor      *blockprocessor.BlockProcessor
	stopBlockProcessing chan struct{}
	logger              *logger.SugarLogger
	sync.Mutex
}

type txProcessorConfig struct {
	db                 worldstate.DB
	blockStore         *blockstore.Store
	txQueueLength      uint32
	txBatchQueueLength uint32
	blockQueueLength   uint32
	maxTxCountPerBatch uint32
	batchTimeout       time.Duration
	logger             *logger.SugarLogger
}

func newTransactionProcessor(conf *txProcessorConfig) (*transactionProcessor, error) {
	p := &transactionProcessor{}

	p.logger = conf.logger
	p.txQueue = queue.New(conf.txQueueLength)
	p.txBatchQueue = queue.New(conf.txBatchQueueLength)
	p.blockQueue = queue.New(conf.blockQueueLength)

	p.txReorderer = txreorderer.New(
		&txreorderer.Config{
			TxQueue:            p.txQueue,
			TxBatchQueue:       p.txBatchQueue,
			MaxTxCountPerBatch: conf.maxTxCountPerBatch,
			BatchTimeout:       conf.batchTimeout,
			Logger:             conf.logger,
		},
	)

	var err error
	if p.blockCreator, err = blockcreator.New(
		&blockcreator.Config{
			TxBatchQueue: p.txBatchQueue,
			BlockQueue:   p.blockQueue,
			Logger:       conf.logger,
			BlockStore:   conf.blockStore,
		},
	); err != nil {
		return nil, err
	}

	p.blockProcessor = blockprocessor.New(
		&blockprocessor.Config{
			BlockQueue: p.blockQueue,
			BlockStore: conf.blockStore,
			DB:         conf.db,
			Logger:     conf.logger,
		},
	)

	// TODO: introduce channel to stop the goroutines (issue #95)
	go p.txReorderer.Run()
	go p.blockCreator.Run()
	p.stopBlockProcessing = make(chan struct{})
	go p.blockProcessor.Run(p.stopBlockProcessing)

	return p, nil
}

// submitTransaction enqueue the transaction to the transaction queue
func (t *transactionProcessor) submitTransaction(tx interface{}) error {
	t.Lock()
	defer t.Unlock()

	if t.txQueue.IsFull() {
		return fmt.Errorf("transaction queue is full. It means the server load is high. Try after sometime")
	}

	jsonBytes, err := json.MarshalIndent(tx, "", "\t")
	if err != nil {
		return fmt.Errorf("failed to marshal transaction: %v", err)
	}
	t.logger.Debugf("enqueuing transaction %s\n", string(jsonBytes))

	t.txQueue.Enqueue(tx)
	t.logger.Debug("transaction is enqueued for re-ordering")

	return nil
}

func (t *transactionProcessor) close() error {
	// TODO: need to signal goroutine to stop
	return nil
}