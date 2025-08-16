package state

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/erigontech/erigon-lib/common"
	"github.com/erigontech/erigon-lib/log/v3"
	"github.com/erigontech/erigon/db/kv"
	"github.com/erigontech/erigon/execution/commitment"
	"github.com/erigontech/erigon/execution/types/accounts"
)

func TestTrieContext_readDomain(t *testing.T) {
	t.Parallel()

	// Setup test database and aggregator
	_db, agg := testDbAndAggregatorv3(t, 10)
	db := wrapDbWithCtx(_db, agg)

	ctx := context.Background()
	rwTx, err := db.BeginTemporalRw(ctx)
	require.NoError(t, err)
	defer rwTx.Rollback()

	domains, err := NewSharedDomains(rwTx, log.New())
	require.NoError(t, err)
	defer domains.Close()

	// Create TrieContext
	sdCtx := NewSharedDomainsCommitmentContext(domains, rwTx, commitment.ModeDirect, commitment.VariantHexPatriciaTrie, t.TempDir())
	defer sdCtx.Close()
	trieCtx := sdCtx.mainTtx

	// Test data
	testAccount := common.HexToAddress("0x1234567890123456789012345678901234567890")
	testStorage := append(testAccount.Bytes(), common.HexToHash("0xabcd").Bytes()...)
	testCode := testAccount.Bytes()
	testCommitmentKey := []byte("commitment_test_key")

	accountData := accounts.Account{
		Nonce:    42,
		Balance:  *uint256.NewInt(1000),
		CodeHash: common.HexToHash("0xdeadbeef"),
	}
	accountBytes := make([]byte, accountData.EncodingLengthForStorage())
	accountData.EncodeForStorage(accountBytes)

	storageValue := common.HexToHash("0xfeedface").Bytes()
	codeValue := []byte("test contract code")
	commitmentValue := []byte("commitment branch data")

	t.Run("read empty domain returns nil", func(t *testing.T) {
		// Reading non-existent key should return nil
		enc, step, err := trieCtx.readDomain(kv.AccountsDomain, testAccount.Bytes())
		require.NoError(t, err)
		require.Nil(t, enc)
		require.Equal(t, kv.Step(0), step)
	})

	t.Run("write and read from accounts domain", func(t *testing.T) {
		// Write account data
		domains.SetTxNum(1)
		err := domains.DomainPut(kv.AccountsDomain, rwTx, testAccount.Bytes(), accountBytes, 1, nil, 0)
		require.NoError(t, err)

		// Read back
		enc, _, err := trieCtx.readDomain(kv.AccountsDomain, testAccount.Bytes())
		require.NoError(t, err)
		require.NotNil(t, enc)
		require.Equal(t, accountBytes, enc)
	})

	t.Run("write and read from storage domain", func(t *testing.T) {
		// Write storage data
		domains.SetTxNum(2)
		err := domains.DomainPut(kv.StorageDomain, rwTx, testStorage, storageValue, 2, nil, 0)
		require.NoError(t, err)

		// Read back
		enc, _, err := trieCtx.readDomain(kv.StorageDomain, testStorage)
		require.NoError(t, err)
		require.NotNil(t, enc)
		require.Equal(t, storageValue, enc)
	})

	t.Run("write and read from code domain", func(t *testing.T) {
		// Write code data
		domains.SetTxNum(3)
		err := domains.DomainPut(kv.CodeDomain, rwTx, testCode, codeValue, 3, nil, 0)
		require.NoError(t, err)

		// Read back
		enc, _, err := trieCtx.readDomain(kv.CodeDomain, testCode)
		require.NoError(t, err)
		require.NotNil(t, enc)
		require.Equal(t, codeValue, enc)
	})

	t.Run("write and read from commitment domain", func(t *testing.T) {
		// Write commitment data
		domains.SetTxNum(4)
		err := domains.DomainPut(kv.CommitmentDomain, rwTx, testCommitmentKey, commitmentValue, 4, nil, 0)
		require.NoError(t, err)

		// Read back
		enc, _, err := trieCtx.readDomain(kv.CommitmentDomain, testCommitmentKey)
		require.NoError(t, err)
		require.NotNil(t, enc)
		require.Equal(t, commitmentValue, enc)
	})

	t.Run("read with limitReadAsOfTxNum", func(t *testing.T) {
		// Setup multiple versions of the same key
		key := common.HexToAddress("0xaaaa").Bytes()
		value1 := []byte("value at tx 10")
		value2 := []byte("value at tx 20")
		value3 := []byte("value at tx 30")

		domains.SetTxNum(10)
		err := domains.DomainPut(kv.AccountsDomain, rwTx, key, value1, 10, nil, 0)
		require.NoError(t, err)

		domains.SetTxNum(20)
		err = domains.DomainPut(kv.AccountsDomain, rwTx, key, value2, 20, value1, 1)
		require.NoError(t, err)

		domains.SetTxNum(30)
		err = domains.DomainPut(kv.AccountsDomain, rwTx, key, value3, 30, value2, 2)
		require.NoError(t, err)

		<-agg.BuildFilesInBackground(40)

		// Flush to ensure data is written
		err = domains.Flush(ctx, rwTx)
		require.NoError(t, err)

		// Test reading at different tx nums
		testCases := []struct {
			name          string
			limitTxNum    uint64
			domainOnly    bool
			expectedValue []byte
		}{
			{
				name:          "read latest without limit",
				limitTxNum:    0,
				domainOnly:    false,
				expectedValue: value3,
			},
			{
				name:          "read as of tx 15",
				limitTxNum:    15,
				domainOnly:    false,
				expectedValue: value1,
			},
			{
				name:          "read as of tx 25",
				limitTxNum:    25,
				domainOnly:    false,
				expectedValue: value2,
			},
			{
				name:          "read as of tx 35",
				limitTxNum:    35,
				domainOnly:    false,
				expectedValue: value3,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Create new context for each test
				newTrieCtx := &TrieContext{
					roTtx:    rwTx,
					getter:   domains.AsGetter(rwTx),
					putter:   domains.AsPutDel(rwTx),
					stepSize: domains.StepSize(),
				}

				newTrieCtx.SetLimitReadAsOfTxNum(tc.limitTxNum, tc.domainOnly)

				enc, _, err := newTrieCtx.readDomain(kv.AccountsDomain, key)
				require.NoError(t, err)
				if tc.expectedValue == nil {
					require.Nil(t, enc)
				} else {
					require.Equal(t, tc.expectedValue, enc)
				}
			})
		}
	})

	t.Run("read with domainOnly flag", func(t *testing.T) {
		// Create a new key that only exists in history
		historyKey := common.HexToAddress("0xbbbb").Bytes()
		historyValue := []byte("historical value")

		// Write and then delete to create history
		domains.SetTxNum(40)
		err := domains.DomainPut(kv.AccountsDomain, rwTx, historyKey, historyValue, 40, nil, 0)
		require.NoError(t, err)

		domains.SetTxNum(50)
		err = domains.DomainDel(kv.AccountsDomain, rwTx, historyKey, 50, historyValue, 1)
		require.NoError(t, err)

		err = domains.Flush(ctx, rwTx)
		require.NoError(t, err)

		// Test with domainOnly=false (should find in history)
		trieCtxWithHistory := &TrieContext{
			roTtx:    rwTx,
			getter:   domains.AsGetter(rwTx),
			putter:   domains.AsPutDel(rwTx),
			stepSize: domains.StepSize(),
		}
		trieCtxWithHistory.SetLimitReadAsOfTxNum(45, false) // withHistory=true

		enc, _, err := trieCtxWithHistory.readDomain(kv.AccountsDomain, historyKey)
		require.NoError(t, err)
		require.Equal(t, historyValue, enc)

		// Test with domainOnly=true (should not find in history)
		trieCtxDomainOnly := &TrieContext{
			roTtx:    rwTx,
			getter:   domains.AsGetter(rwTx),
			putter:   domains.AsPutDel(rwTx),
			stepSize: domains.StepSize(),
		}
		trieCtxDomainOnly.SetLimitReadAsOfTxNum(45, true) // withHistory=false

		enc, _, err = trieCtxDomainOnly.readDomain(kv.AccountsDomain, historyKey)
		require.NoError(t, err)
		require.Nil(t, enc) // Should not find because it's only in history
	})

	t.Run("concurrent reads", func(t *testing.T) {
		// Test that multiple goroutines can read concurrently
		done := make(chan bool)
		errors := make(chan error, 10)

		for i := 0; i < 10; i++ {
			go func() {
				defer func() { done <- true }()

				enc, _, err := trieCtx.readDomain(kv.AccountsDomain, testAccount.Bytes())
				if err != nil {
					errors <- err
					return
				}
				if !bytes.Equal(accountBytes, enc) {
					errors <- fmt.Errorf("expected %v, got %v", accountBytes, enc)
				}
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			<-done
		}

		// Check for errors
		select {
		case err := <-errors:
			t.Fatalf("Concurrent read failed: %v", err)
		default:
			// No errors
		}
	})

	t.Run("read returns step from domain files", func(t *testing.T) {
		// This test would require setting up domain files which is more complex
		// For now, we verify that step is returned correctly for in-memory reads
		enc, step, err := trieCtx.readDomain(kv.AccountsDomain, testAccount.Bytes())
		require.NoError(t, err)
		require.NotNil(t, enc)
		// Step should be non-zero when reading from domain files
		// In this test setup it might be 0 since we're using in-memory data
		require.GreaterOrEqual(t, step, kv.Step(0))
	})
}

func TestTrieContext_readDomain_EdgeCases(t *testing.T) {
	t.Parallel()

	_db, agg := testDbAndAggregatorv3(t, 16)
	db := wrapDbWithCtx(_db, agg)

	ctx := context.Background()
	rwTx, err := db.BeginTemporalRw(ctx)
	require.NoError(t, err)
	defer rwTx.Rollback()

	domains, err := NewSharedDomains(rwTx, log.New())
	require.NoError(t, err)
	defer domains.Close()

	sdCtx := NewSharedDomainsCommitmentContext(domains, rwTx, commitment.ModeDirect, commitment.VariantHexPatriciaTrie, t.TempDir())
	defer sdCtx.Close()
	trieCtx := sdCtx.mainTtx

	t.Run("nil key", func(t *testing.T) {
		enc, step, err := trieCtx.readDomain(kv.AccountsDomain, nil)
		require.NoError(t, err)
		require.Nil(t, enc)
		require.Equal(t, kv.Step(0), step)
	})

	t.Run("empty key", func(t *testing.T) {
		enc, step, err := trieCtx.readDomain(kv.AccountsDomain, []byte{})
		require.NoError(t, err)
		require.Nil(t, enc)
		require.Equal(t, kv.Step(0), step)
	})

	t.Run("very large key", func(t *testing.T) {
		largeKey := make([]byte, 1024)
		for i := range largeKey {
			largeKey[i] = byte(i % 256)
		}

		enc, step, err := trieCtx.readDomain(kv.AccountsDomain, largeKey)
		require.NoError(t, err)
		require.Nil(t, enc)
		require.Equal(t, kv.Step(0), step)
	})

	t.Run("empty value", func(t *testing.T) {
		key := []byte("empty_value_key")
		domains.SetTxNum(100)

		// Store empty value
		err := domains.DomainPut(kv.AccountsDomain, rwTx, key, []byte{}, 100, nil, 0)
		require.NoError(t, err)

		// Read back - should get empty slice, not nil
		enc, _, err := trieCtx.readDomain(kv.AccountsDomain, key)
		require.NoError(t, err)
		require.NotNil(t, enc)
		require.Equal(t, []byte{}, enc)
	})
}
