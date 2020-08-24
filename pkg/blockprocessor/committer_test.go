package blockprocessor

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang/protobuf/proto"
	"github.com/stretchr/testify/require"
	"github.ibm.com/blockchaindb/protos/types"
	"github.ibm.com/blockchaindb/server/pkg/blockstore"
	"github.ibm.com/blockchaindb/server/pkg/identity"
	"github.ibm.com/blockchaindb/server/pkg/worldstate"
	"github.ibm.com/blockchaindb/server/pkg/worldstate/leveldb"
)

type committerTestEnv struct {
	db              *leveldb.LevelDB
	dbPath          string
	blockStore      *blockstore.Store
	blockStorePath  string
	identityQuerier *identity.Querier
	committer       *committer
	cleanup         func()
}

func newCommitterTestEnv(t *testing.T) *committerTestEnv {
	dir, err := ioutil.TempDir("/tmp", "committer")
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "leveldb")
	db, err := leveldb.New(dbPath)
	if err != nil {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			t.Errorf("error while removing directory %s, %v", dir, rmErr)
		}
		t.Fatalf("error while creating leveldb, %v", err)
	}

	blockStorePath := filepath.Join(dir, "blockstore")
	blockStore, err := blockstore.Open(blockStorePath)
	if err != nil {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			t.Errorf("error while removing directory %s, %v", dir, rmErr)
		}
		t.Fatalf("error while creating blockstore, %v", err)
	}

	cleanup := func() {
		if err := db.Close(); err != nil {
			t.Errorf("error while closing the db instance, %v", err)
		}

		if err := blockStore.Close(); err != nil {
			t.Errorf("error while closing blockstore, %v", err)
		}

		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("error while removing directory %s, %v", dir, err)
		}
	}

	c := &Config{
		DB:         db,
		BlockStore: blockStore,
	}
	return &committerTestEnv{
		db:              db,
		dbPath:          dbPath,
		blockStore:      blockStore,
		blockStorePath:  blockStorePath,
		identityQuerier: identity.NewQuerier(db),
		committer:       newCommitter(c),
		cleanup:         cleanup,
	}
}

func TestCommitter(t *testing.T) {
	t.Run("commit block to block store and state db", func(t *testing.T) {
		env := newCommitterTestEnv(t)
		defer env.cleanup()

		env.db.Create("db1")

		block1 := &types.Block{
			Header: &types.BlockHeader{
				Number: 1,
			},
			TransactionEnvelopes: []*types.TransactionEnvelope{
				{
					Payload: &types.Transaction{
						DBName: "db1",
						Writes: []*types.KVWrite{
							{
								Key:   "db1-key1",
								Value: []byte("value-1"),
							},
						},
					},
				},
			},
		}

		err := env.committer.commitBlock(
			block1,
			[]*types.ValidationInfo{
				{
					Flag: types.Flag_VALID,
				},
			},
		)
		require.NoError(t, err)

		height, err := env.blockStore.Height()
		require.NoError(t, err)
		require.Equal(t, uint64(1), height)

		block, err := env.blockStore.Get(1)
		require.NoError(t, err)
		require.True(t, proto.Equal(block, block1))

		val, metadata, err := env.db.Get("db1", "db1-key1")
		require.NoError(t, err)

		expectedMetadata := &types.Metadata{
			Version: &types.Version{
				BlockNum: 1,
				TxNum:    0,
			},
		}
		require.True(t, proto.Equal(expectedMetadata, metadata))
		require.Equal(t, val, []byte("value-1"))
	})
}

func TestBlockStoreCommitter(t *testing.T) {
	getSampleBlock := func(number uint64) *types.Block {
		return &types.Block{
			Header: &types.BlockHeader{
				Number: number,
			},
			TransactionEnvelopes: []*types.TransactionEnvelope{
				{
					Payload: &types.Transaction{
						DBName: "db1",
						Writes: []*types.KVWrite{
							{
								Key:   fmt.Sprintf("db1-key%d", number),
								Value: []byte(fmt.Sprintf("new-value-%d", number)),
							},
						},
					},
				},
				{
					Payload: &types.Transaction{
						DBName: "db2",
						Writes: []*types.KVWrite{
							{
								Key:   fmt.Sprintf("db2-key%d", number),
								Value: []byte(fmt.Sprintf("new-value-%d", number)),
							},
						},
					},
				},
			},
		}
	}

	t.Run("commit multiple blocks to the block store and query the same", func(t *testing.T) {
		env := newCommitterTestEnv(t)
		defer env.cleanup()

		var expectedBlocks []*types.Block

		for blockNumber := uint64(1); blockNumber <= 1000; blockNumber++ {
			block := getSampleBlock(blockNumber)
			require.NoError(t, env.committer.commitToBlockStore(block))
			expectedBlocks = append(expectedBlocks, block)
		}

		for blockNumber := uint64(1); blockNumber <= 1000; blockNumber++ {
			block, err := env.blockStore.Get(blockNumber)
			require.NoError(t, err)
			require.True(t, proto.Equal(expectedBlocks[blockNumber-1], block))
		}

		height, err := env.blockStore.Height()
		require.NoError(t, err)
		require.Equal(t, uint64(1000), height)
	})

	t.Run("commit unexpected block to the block store", func(t *testing.T) {
		env := newCommitterTestEnv(t)
		defer env.cleanup()

		block := getSampleBlock(10)
		err := env.committer.commitToBlockStore(block)
		require.EqualError(t, err, "expected block number [1] but received [10]")
	})
}

func TestStateDBCommitterForData(t *testing.T) {
	t.Parallel()

	setup := func(db worldstate.DB) []*worldstate.DBUpdates {
		dbsUpdates := []*worldstate.DBUpdates{
			{
				DBName: "db1",
				Writes: []*worldstate.KVWithMetadata{
					{
						Key:   "db1-key1",
						Value: []byte("db1-value1"),
						Metadata: &types.Metadata{
							Version: &types.Version{
								BlockNum: 1,
								TxNum:    1,
							},
						},
					},
					{
						Key:   "db1-key2",
						Value: []byte("db1-value2"),
						Metadata: &types.Metadata{
							Version: &types.Version{
								BlockNum: 1,
								TxNum:    2,
							},
						},
					},
				},
			},
			{
				DBName: "db2",
				Writes: []*worldstate.KVWithMetadata{
					{
						Key:   "db2-key1",
						Value: []byte("db2-value1"),
						Metadata: &types.Metadata{
							Version: &types.Version{
								BlockNum: 1,
								TxNum:    3,
							},
						},
					},
					{
						Key:   "db2-key2",
						Value: []byte("db2-value2"),
						Metadata: &types.Metadata{
							Version: &types.Version{
								BlockNum: 1,
								TxNum:    4,
							},
						},
					},
				},
			},
		}
		require.NoError(t, db.Create("db1"))
		require.NoError(t, db.Create("db2"))
		require.NoError(t, db.Commit(dbsUpdates))
		return dbsUpdates
	}

	t.Run("commit block to replace all existing entries", func(t *testing.T) {
		t.Parallel()
		env := newCommitterTestEnv(t)
		defer env.cleanup()
		initialKVsPerDB := setup(env.db)

		for _, kvs := range initialKVsPerDB {
			for _, kv := range kvs.Writes {
				val, metadata, err := env.db.Get(kvs.DBName, kv.Key)
				require.NoError(t, err)
				require.Equal(t, kv.Value, val)
				require.True(t, proto.Equal(kv.Metadata, metadata))
			}
		}

		// create a block to update all existing entries in the database
		// In db1, we update db1-key1, db1-key2
		// In db2, we update db2-key1, db2-key2
		block := &types.Block{
			Header: &types.BlockHeader{
				Number: 2,
			},
			TransactionEnvelopes: []*types.TransactionEnvelope{
				{
					Payload: &types.Transaction{
						DBName: "db1",
						Writes: []*types.KVWrite{
							{
								Key:   "db1-key1",
								Value: []byte("new-value-1"),
							},
						},
					},
				},
				{
					Payload: &types.Transaction{
						DBName: "db1",
						Writes: []*types.KVWrite{
							{
								Key:   "db1-key2",
								Value: []byte("new-value-2"),
							},
						},
					},
				},
				{
					Payload: &types.Transaction{
						DBName: "db2",
						Writes: []*types.KVWrite{
							{
								Key:   "db2-key1",
								Value: []byte("new-value-1"),
							},
						},
					},
				},
				{
					Payload: &types.Transaction{
						DBName: "db2",
						Writes: []*types.KVWrite{
							{
								Key:   "db2-key2",
								Value: []byte("new-value-2"),
							},
						},
					},
				},
			},
		}

		validationInfo := []*types.ValidationInfo{
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_VALID,
			},
		}

		require.NoError(t, env.committer.commitToStateDB(block, validationInfo))

		// as the last block commit has updated all existing entries,
		// kvs in initialKVsPerDB should not match with the committed versions
		for _, kvs := range initialKVsPerDB {
			for _, kv := range kvs.Writes {
				val, metadata, err := env.db.Get(kvs.DBName, kv.Key)
				require.NoError(t, err)
				require.NotEqual(t, kv.Value, val)
				require.False(t, proto.Equal(kv.Metadata, metadata))
			}
		}

		val, metadata, err := env.db.Get("db1", "db1-key1")
		require.NoError(t, err)
		expectedVal := []byte("new-value-1")
		expectedMetadata := &types.Metadata{
			Version: &types.Version{
				BlockNum: 2,
				TxNum:    0,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		val, metadata, err = env.db.Get("db1", "db1-key2")
		require.NoError(t, err)
		expectedVal = []byte("new-value-2")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 2,
				TxNum:    1,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		val, metadata, err = env.db.Get("db2", "db2-key1")
		require.NoError(t, err)
		expectedVal = []byte("new-value-1")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 2,
				TxNum:    2,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		val, metadata, err = env.db.Get("db2", "db2-key2")
		require.NoError(t, err)
		expectedVal = []byte("new-value-2")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 2,
				TxNum:    3,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))
	})

	t.Run("commit block to delete all existing entries", func(t *testing.T) {
		t.Parallel()
		env := newCommitterTestEnv(t)
		defer env.cleanup()
		initialKVsPerDB := setup(env.db)

		for _, kvs := range initialKVsPerDB {
			for _, kv := range kvs.Writes {
				val, metadata, err := env.db.Get(kvs.DBName, kv.Key)
				require.NoError(t, err)
				require.Equal(t, kv.Value, val)
				require.True(t, proto.Equal(kv.Metadata, metadata))
			}
		}

		// create a block to delete all existing entries in the database
		// In db1, we delete db1-key1, db1-key2
		// In db2, we delete db2-key1, db2-key2
		block := &types.Block{
			Header: &types.BlockHeader{
				Number: 2,
			},
			TransactionEnvelopes: []*types.TransactionEnvelope{
				{
					Payload: &types.Transaction{
						DBName: "db1",
						Writes: []*types.KVWrite{
							{
								Key:      "db1-key1",
								IsDelete: true,
							},
							{
								Key:      "db1-key2",
								IsDelete: true,
							},
						},
					},
				},
				{
					Payload: &types.Transaction{
						DBName: "db2",
						Writes: []*types.KVWrite{
							{
								Key:      "db2-key1",
								IsDelete: true,
							},
							{
								Key:      "db2-key2",
								IsDelete: true,
							},
						},
					},
				},
			},
		}

		validationInfo := []*types.ValidationInfo{
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_VALID,
			},
		}

		require.NoError(t, env.committer.commitToStateDB(block, validationInfo))

		// as the last block commit has deleted all existing entries,
		// kvs in initialKVsPerDB should not match with the committed versions
		for _, kvs := range initialKVsPerDB {
			for _, kv := range kvs.Writes {
				val, metadata, err := env.db.Get(kvs.DBName, kv.Key)
				require.NoError(t, err)
				require.NotEqual(t, kv.Value, val)
				require.False(t, proto.Equal(kv.Metadata, metadata))
			}
		}

		val, metadata, err := env.db.Get("db1", "db1-key1")
		require.NoError(t, err)
		require.Nil(t, val)
		require.Nil(t, metadata)

		val, metadata, err = env.db.Get("db1", "db1-key2")
		require.NoError(t, err)
		require.Nil(t, val)
		require.Nil(t, metadata)

		val, metadata, err = env.db.Get("db1", "db2-key1")
		require.NoError(t, err)
		require.Nil(t, val)
		require.Nil(t, metadata)

		val, metadata, err = env.db.Get("db1", "db2-key2")
		require.NoError(t, err)
		require.Nil(t, val)
		require.Nil(t, metadata)
	})

	t.Run("commit block to only insert new entries", func(t *testing.T) {
		t.Parallel()
		env := newCommitterTestEnv(t)
		defer env.cleanup()
		initialKVsPerDB := setup(env.db)

		for _, kvs := range initialKVsPerDB {
			for _, kv := range kvs.Writes {
				val, metadata, err := env.db.Get(kvs.DBName, kv.Key)
				require.NoError(t, err)
				require.Equal(t, kv.Value, val)
				require.True(t, proto.Equal(kv.Metadata, metadata))
			}
		}

		// create a block to insert new entries without touching the
		// existing entries in the database
		// In db1, insert db1-key3, db1-key4
		// In db2, insert db2-key3, db2-key4
		block := &types.Block{
			Header: &types.BlockHeader{
				Number: 2,
			},
			TransactionEnvelopes: []*types.TransactionEnvelope{
				{
					Payload: &types.Transaction{
						DBName: "db1",
						Writes: []*types.KVWrite{
							{
								Key:   "db1-key3",
								Value: []byte("value-3"),
							},
							{
								Key:   "db1-key4",
								Value: []byte("value-4"),
							},
						},
					},
				},
				{
					Payload: &types.Transaction{
						DBName: "db2",
						Writes: []*types.KVWrite{
							{
								Key:   "db2-key3",
								Value: []byte("value-3"),
							},
							{
								Key:   "db2-key4",
								Value: []byte("value-4"),
							},
						},
					},
				},
			},
		}

		validationInfo := []*types.ValidationInfo{
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_VALID,
			},
		}

		require.NoError(t, env.committer.commitToStateDB(block, validationInfo))

		// as the last block commit has not modified existing entries,
		// kvs in initialKVsPerDB should match with the committed versions
		for _, kvs := range initialKVsPerDB {
			for _, kv := range kvs.Writes {
				val, metadata, err := env.db.Get(kvs.DBName, kv.Key)
				require.NoError(t, err)
				require.Equal(t, kv.Value, val)
				require.True(t, proto.Equal(kv.Metadata, metadata))
			}
		}

		val, metadata, err := env.db.Get("db1", "db1-key3")
		require.NoError(t, err)
		expectedVal := []byte("value-3")
		expectedMetadata := &types.Metadata{
			Version: &types.Version{
				BlockNum: 2,
				TxNum:    0,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		val, metadata, err = env.db.Get("db1", "db1-key4")
		require.NoError(t, err)
		expectedVal = []byte("value-4")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 2,
				TxNum:    0,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		val, metadata, err = env.db.Get("db2", "db2-key3")
		require.NoError(t, err)
		expectedVal = []byte("value-3")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 2,
				TxNum:    1,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		val, metadata, err = env.db.Get("db2", "db2-key4")
		require.NoError(t, err)
		expectedVal = []byte("value-4")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 2,
				TxNum:    1,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))
	})

	t.Run("commit block to update and delete existing entries while inserting new", func(t *testing.T) {
		t.Parallel()
		env := newCommitterTestEnv(t)
		defer env.cleanup()
		initialKVsPerDB := setup(env.db)

		for _, kvs := range initialKVsPerDB {
			for _, kv := range kvs.Writes {
				val, metadata, err := env.db.Get(kvs.DBName, kv.Key)
				require.NoError(t, err)
				require.Equal(t, kv.Value, val)
				require.True(t, proto.Equal(kv.Metadata, metadata))
			}
		}

		// create a block to update & delete existing entries in the database
		// add a new entry
		// In db1, we delete db1-key1, update db1-key2, newly add db1-key3
		// In db2, we update db2-key1, delete db2-key2, newly add db2-key3
		block := &types.Block{
			Header: &types.BlockHeader{
				Number: 10,
			},
			TransactionEnvelopes: []*types.TransactionEnvelope{
				{
					// we mark this transaction valid
					Payload: &types.Transaction{
						DBName: "db1",
						Writes: []*types.KVWrite{
							{
								Key:      "db1-key1",
								IsDelete: true,
							},
							{
								Key:   "db1-key2",
								Value: []byte("new-value-2"),
							},
							{
								Key:   "db1-key3",
								Value: []byte("value-3"),
							},
							{
								Key:      "db1-key4",
								IsDelete: true,
							},
						},
					},
				},
				{
					// we mark this transaction invalid
					Payload: &types.Transaction{
						DBName: "db3",
						Writes: []*types.KVWrite{
							{
								Key:   "db3-key2",
								Value: []byte("value-2"),
							},
						},
					},
				},
				{
					// we mark this transaction valid
					Payload: &types.Transaction{
						DBName: "db2",
						Reads: []*types.KVRead{
							{
								Key: "db2-key1",
								Version: &types.Version{
									BlockNum: 1,
									TxNum:    3,
								},
							},
						},
						Writes: []*types.KVWrite{
							{
								Key:   "db2-key1",
								Value: []byte("new-value-1"),
							},
						},
					},
				},
				{
					// we mark this transaction valid
					Payload: &types.Transaction{
						DBName: "db2",
						Writes: []*types.KVWrite{
							{
								Key:      "db2-key2",
								IsDelete: true,
							},
							{
								Key:   "db2-key3",
								Value: []byte("value-3"),
							},
						},
					},
				},
				{
					// we mark this transaction valid
					Payload: &types.Transaction{
						DBName: "db2",
						Writes: []*types.KVWrite{},
					},
				},
				{
					// we mark this transaction invalid
					Payload: &types.Transaction{
						DBName: "db2",
						Writes: []*types.KVWrite{
							{
								Key:      "db2-key2",
								IsDelete: true,
							},
							{
								Key:   "db2-key3",
								Value: []byte("value-3"),
							},
						},
					},
				},
			},
		}

		validationInfo := []*types.ValidationInfo{
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_INVALID_DB_NOT_EXIST,
			},
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_INVALID_MVCC_CONFLICT,
			},
		}

		require.NoError(t, env.committer.commitToStateDB(block, validationInfo))

		// as the last block commit has either updated or deleted
		// existing entries, kvs in initialKVsPerDB should not
		// match with the committed versions
		for _, kvs := range initialKVsPerDB {
			for _, kv := range kvs.Writes {
				val, metadata, err := env.db.Get(kvs.DBName, kv.Key)
				require.NoError(t, err)
				require.NotEqual(t, kv.Value, val)
				require.False(t, proto.Equal(kv.Metadata, metadata))
			}
		}

		// In db1, we delete db1-key1, update db1-key2, newly add db1-key3
		val, metadata, err := env.db.Get("db1", "db1-key1")
		require.NoError(t, err)
		require.Nil(t, val)
		require.Nil(t, metadata)

		val, metadata, err = env.db.Get("db1", "db1-key2")
		require.NoError(t, err)
		expectedVal := []byte("new-value-2")
		expectedMetadata := &types.Metadata{
			Version: &types.Version{
				BlockNum: 10,
				TxNum:    0,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		val, metadata, err = env.db.Get("db1", "db1-key3")
		require.NoError(t, err)
		expectedVal = []byte("value-3")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 10,
				TxNum:    0,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		// In db2, we update db2-key1, delete db2-key2, newly add db2-key3
		val, metadata, err = env.db.Get("db2", "db2-key1")
		require.NoError(t, err)
		expectedVal = []byte("new-value-1")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 10,
				TxNum:    2,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))

		val, metadata, err = env.db.Get("db2", "db2-key2")
		require.NoError(t, err)
		require.Nil(t, val)

		val, metadata, err = env.db.Get("db2", "db2-key3")
		require.NoError(t, err)
		expectedVal = []byte("value-3")
		expectedMetadata = &types.Metadata{
			Version: &types.Version{
				BlockNum: 10,
				TxNum:    3,
			},
		}
		require.Equal(t, expectedVal, val)
		require.True(t, proto.Equal(expectedMetadata, metadata))
	})

	t.Run("commit block and expect error", func(t *testing.T) {
		t.Parallel()
		env := newCommitterTestEnv(t)
		defer env.cleanup()

		block := &types.Block{
			Header: &types.BlockHeader{
				Number: 2,
			},
			TransactionEnvelopes: []*types.TransactionEnvelope{
				{
					Payload: &types.Transaction{
						DBName: "db1",
						Writes: []*types.KVWrite{
							{
								Key:   "db1-key3",
								Value: []byte("value-3"),
							},
						},
					},
				},
			},
		}

		validationInfo := []*types.ValidationInfo{
			{
				Flag: types.Flag_VALID,
			},
		}

		require.EqualError(t, env.committer.commitToStateDB(block, validationInfo), "failed to commit block 2 to state database: database db1 does not exist")
	})
}

func TestStateDBCommitterForUsers(t *testing.T) {
	t.Parallel()

	getSampleBlock := func(number uint64) (*types.Block, []*types.ValidationInfo, []*types.User) {
		userWithLessPrivilege := &types.User{
			ID:          fmt.Sprintf("%s:%d", "userWithLessPrivilege", number),
			Certificate: []byte("certificate-1"),
			Privilege: &types.Privilege{
				DBPermission: map[string]types.Privilege_Access{
					fmt.Sprintf("db-%d", number): types.Privilege_Read,
				},
				DBAdministration:      false,
				ClusterAdministration: false,
				UserAdministration:    false,
			},
		}

		userWithMorePrivilege := &types.User{
			ID:          fmt.Sprintf("%s:%d", "userWithMorePrivilege", number),
			Certificate: []byte("certificate-2"),
			Privilege: &types.Privilege{
				DBPermission: map[string]types.Privilege_Access{
					fmt.Sprintf("db-%d", number): types.Privilege_ReadWrite,
				},
				DBAdministration:      true,
				ClusterAdministration: true,
				UserAdministration:    true,
			},
		}

		user1, err := proto.Marshal(userWithLessPrivilege)
		require.NoError(t, err)
		user2, err := proto.Marshal(userWithMorePrivilege)
		require.NoError(t, err)

		block := &types.Block{
			Header: &types.BlockHeader{
				Number: number,
			},
			TransactionEnvelopes: []*types.TransactionEnvelope{
				{
					Payload: &types.Transaction{
						DBName: worldstate.UsersDBName,
						Writes: []*types.KVWrite{
							{
								Key:   fmt.Sprintf("%s:%d", "userWithLessPrivilege", number),
								Value: user1,
							},
						},
					},
				},
				{
					Payload: &types.Transaction{
						DBName: worldstate.UsersDBName,
						Writes: []*types.KVWrite{
							{
								Key:   fmt.Sprintf("%s:%d", "userWithMorePrivilege", number),
								Value: user2,
							},
						},
					},
				},
			},
		}

		valInfo := []*types.ValidationInfo{
			{
				Flag: types.Flag_VALID,
			},
			{
				Flag: types.Flag_VALID,
			},
		}

		return block, valInfo, []*types.User{
			userWithLessPrivilege,
			userWithMorePrivilege,
		}
	}

	t.Run("commit block with all valid transactions", func(t *testing.T) {
		t.Parallel()

		env := newCommitterTestEnv(t)
		defer env.cleanup()

		block, valInfo, users := getSampleBlock(1)
		require.NoError(t, env.committer.commitToStateDB(block, valInfo))

		for i, expectedUser := range users {
			persistedUser, metadata, err := env.identityQuerier.GetUser(expectedUser.ID)
			require.NoError(t, err)

			expectedMetadata := &types.Metadata{
				Version: &types.Version{
					BlockNum: 1,
					TxNum:    uint64(i),
				},
			}
			require.True(t, proto.Equal(expectedMetadata, metadata))
			require.True(t, proto.Equal(expectedUser, persistedUser))
		}
	})

	t.Run("commit block with a mix of valid and invalid transactions", func(t *testing.T) {
		t.Parallel()

		env := newCommitterTestEnv(t)
		defer env.cleanup()

		block, valInfo, users := getSampleBlock(1)
		valInfo[0] = &types.ValidationInfo{
			Flag: types.Flag_INVALID_NO_PERMISSION,
		}
		require.NoError(t, env.committer.commitToStateDB(block, valInfo))

		persistedUser, metadata, err := env.identityQuerier.GetUser(users[0].ID)
		require.NoError(t, err)
		require.Nil(t, metadata)
		require.Nil(t, persistedUser)

		persistedUser, metadata, err = env.identityQuerier.GetUser(users[1].ID)
		require.NoError(t, err)

		expectedMetadata := &types.Metadata{
			Version: &types.Version{
				BlockNum: 1,
				TxNum:    1,
			},
		}
		require.True(t, proto.Equal(expectedMetadata, metadata))
		require.True(t, proto.Equal(users[1], persistedUser))
	})
}
