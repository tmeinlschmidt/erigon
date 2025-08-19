// Copyright 2018 The go-ethereum Authors
// (original work)
// Copyright 2024 The Erigon Authors
// (modifications)
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/erigontech/erigon-lib/chain"
	"github.com/erigontech/erigon-lib/common"
	"github.com/erigontech/erigon-lib/kv"
	"github.com/erigontech/erigon/execution/stagedsync/stages"
	"github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/polygon/bor/borcfg"
)

// ReadChainConfig retrieves the consensus settings based on the given genesis hash.
func ReadChainConfig(db kv.Getter, hash common.Hash) (*chain.Config, error) {
	data, err := db.GetOne(kv.ConfigTable, hash[:])
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	var config chain.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("invalid chain config JSON: %x, %w", hash, err)
	}

	if config.BorJSON != nil {
		borConfig := &borcfg.BorConfig{}
		if err := json.Unmarshal(config.BorJSON, borConfig); err != nil {
			return nil, fmt.Errorf("invalid chain config 'bor' JSON: %x, %w", hash, err)
		}
		config.Bor = borConfig
	}
	return &config, nil
}

// WriteChainConfig writes the chain config settings to the database.
func WriteChainConfig(db kv.Putter, hash common.Hash, cfg *chain.Config) error {
	if cfg == nil {
		return nil
	}

	if cfg.Bor != nil {
		borJSON, err := json.Marshal(cfg.Bor)
		if err != nil {
			return fmt.Errorf("failed to JSON encode chain config 'bor': %w", err)
		}
		cfg.BorJSON = borJSON
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to JSON encode chain config: %w", err)
	}

	if err := db.Put(kv.ConfigTable, hash[:], data); err != nil {
		return fmt.Errorf("failed to store chain config: %w", err)
	}
	return nil
}

func WriteGenesisIfNotExist(db kv.RwTx, g *types.Genesis) error {
	has, err := db.Has(kv.ConfigTable, kv.GenesisKey)
	if err != nil {
		return err
	}
	if has {
		return nil
	}

	// Marshal json g
	val, err := json.Marshal(g)
	if err != nil {
		return err
	}

	// Compress the JSON data
	compressed, err := compressData(val)
	if err != nil {
		return fmt.Errorf("failed to compress genesis: %w", err)
	}

	return db.Put(kv.ConfigTable, kv.GenesisKey, compressed)
}

func ReadGenesis(db kv.Getter) (*types.Genesis, error) {
	val, err := db.GetOne(kv.ConfigTable, kv.GenesisKey)
	if err != nil {
		return nil, err
	}
	if len(val) == 0 {
		return nil, nil
	}

	// Try to decompress the data first
	decompressed, err := decompressData(val)
	if err != nil {
		// If decompression fails, try to read as uncompressed (for backward compatibility)
		if string(val) == "null" {
			return nil, nil
		}
		var g types.Genesis
		if err := json.Unmarshal(val, &g); err != nil {
			return nil, fmt.Errorf("failed to unmarshal genesis: %w", err)
		}
		return &g, nil
	}

	if string(decompressed) == "null" {
		return nil, nil
	}

	var g types.Genesis
	if err := json.Unmarshal(decompressed, &g); err != nil {
		return nil, fmt.Errorf("failed to unmarshal genesis: %w", err)
	}
	return &g, nil
}

// compressData compresses the input data using gzip
func compressData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decompressData decompresses the input data using gzip
func decompressData(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	decompressed, err := io.ReadAll(gr)
	if err != nil {
		return nil, err
	}
	return decompressed, nil
}

func AllSegmentsDownloadComplete(tx kv.Getter) (allSegmentsDownloadComplete bool, err error) {
	snapshotsStageProgress, err := stages.GetStageProgress(tx, stages.Snapshots)
	return snapshotsStageProgress > 0, err
}
func AllSegmentsDownloadCompleteFromDB(db kv.RoDB) (allSegmentsDownloadComplete bool, err error) {
	err = db.View(context.Background(), func(tx kv.Tx) error {
		allSegmentsDownloadComplete, err = AllSegmentsDownloadComplete(tx)
		return err
	})
	return allSegmentsDownloadComplete, err
}
