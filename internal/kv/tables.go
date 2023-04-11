// Copyright 2022 The AmazeChain Authors
// This file is part of the AmazeChain library.
//
// The AmazeChain library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The AmazeChain library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the AmazeChain library. If not, see <http://www.gnu.org/licenses/>.

package kv

import (
	"sort"
	"strings"
)

// DBSchemaVersion versions list
// 5.0 - BlockTransaction table now has canonical ids (txs of non-canonical blocks moving to NonCanonicalTransaction table)
// 6.0 - BlockTransaction table now has system-txs before and after block (records are absent if block has no system-tx, but sequence increasing)

// ChaindataTables

// Dictionary:
// "Plain State" - state where keys arent' hashed. "CurrentState" - same, but keys are hashed. "PlainState" used for blocks execution. "CurrentState" used mostly for Merkle root calculation.
// "incarnation" - uint64 number - how much times given account was SelfDestruct'ed.

/*
PlainState logical layout:

	Contains Accounts:
	  key - address (unhashed)
	  value - account encoded for storage
	Contains Storage:
	  key - address (unhashed) + incarnation + storage key (unhashed)
	  value - storage value(common.hash)

Physical layout:

	PlainState and HashedStorage utilises DupSort feature of MDBX (store multiple values inside 1 key).

-------------------------------------------------------------

	key              |            value

-------------------------------------------------------------
[acc_hash]              | [acc_value]
[acc_hash]+[inc]        | [storage1_hash]+[storage1_value]

	| [storage2_hash]+[storage2_value] // this value has no own key. it's 2nd value of [acc_hash]+[inc] key.
	| [storage3_hash]+[storage3_value]
	| ...

[acc_hash]+[old_inc]    | [storage1_hash]+[storage1_value]

	| ...

[acc2_hash]             | [acc2_value]

	...
*/
const PlainState = "PlainState"

// PlainContractCode -
// key - address+incarnation
// value - code hash
const PlainContractCode = "PlainCodeHash"

/*
AccountChangeSet and StorageChangeSet - of block N store values of state before block N changed them.
Because values "after" change stored in PlainState.
Logical format:

	key - blockNum_u64 + key_in_plain_state
	value - value_in_plain_state_before_blockNum_changes

Example: If block N changed account A from value X to Y. Then:

	AccountChangeSet has record: bigEndian(N) + A -> X
	PlainState has record: A -> Y

See also: docs/programmers_guide/db_walkthrough.MD#table-history-of-accounts

As you can see if block N changes much accounts - then all records have repetitive prefix `bigEndian(N)`.
MDBX can store such prefixes only once - by DupSort feature (see `docs/programmers_guide/dupsort.md`).
Both buckets are DupSort-ed and have physical format:
AccountChangeSet:

	key - blockNum_u64
	value - address + account(encoded)

StorageChangeSet:

	key - blockNum_u64 + address + incarnation_u64
	value - plain_storage_key + value
*/
const AccountChangeSet = "AccountChangeSet"
const StorageChangeSet = "StorageChangeSet"

const (

	//HashedAccounts
	// key - address hash
	// value - account encoded for storage
	// Contains Storage:
	//key - address hash + incarnation + storage key hash
	//value - storage value(common.hash)
	HashedAccounts = "HashedAccount"
	HashedStorage  = "HashedStorage"
)

/*
AccountsHistory and StorageHistory - indices designed to serve next 2 type of requests:
1. what is smallest block number >= X where account A changed
2. get last shard of A - to append there new block numbers

Task 1. is part of "get historical state" operation (see `core/state:GetAsOf`):
If `db.Seek(A+bigEndian(X))` returns non-last shard -

	then get block number from shard value Y := RoaringBitmap(shard_value).GetGte(X)
	and with Y go to ChangeSets: db.Get(ChangeSets, Y+A)

If `db.Seek(A+bigEndian(X))` returns last shard -

	then we go to PlainState: db.Get(PlainState, A)

Format:
  - index split to shards by 2Kb - RoaringBitmap encoded sorted list of block numbers
    (to avoid performance degradation of popular accounts or look deep into history.
    Also 2Kb allows avoid Overflow pages inside DB.)
  - if shard is not last - then key has suffix 8 bytes = bigEndian(max_block_num_in_this_shard)
  - if shard is last - then key has suffix 8 bytes = 0xFF

It allows:
  - server task 1. by 1 db operation db.Seek(A+bigEndian(X))
  - server task 2. by 1 db operation db.Get(A+0xFF)

see also: docs/programmers_guide/db_walkthrough.MD#table-change-sets

AccountsHistory:

	key - address + shard_id_u64
	value - roaring bitmap  - list of block where it changed

StorageHistory

	key - address + storage_key + shard_id_u64
	value - roaring bitmap - list of block where it changed
*/
const AccountsHistory = "AccountHistory"
const StorageHistory = "StorageHistory"

const (

	//key - contract code hash
	//value - contract code
	Code = "Code"

	//key - addressHash+incarnation
	//value - code hash
	ContractCode = "HashedCodeHash"

	// IncarnationMap for deleted accounts
	//key - address
	//value - incarnation of account when it was last deleted
	IncarnationMap = "IncarnationMap"

	//TEVMCode -
	//key - contract code hash
	//value - contract TEVM code
	ContractTEVMCode = "TEVMCode"
)

/*
TrieOfAccounts and TrieOfStorage
hasState,groups - mark prefixes existing in hashed_account table
hasTree - mark prefixes existing in trie_account table (not related with branchNodes)
hasHash - mark prefixes which hashes are saved in current trie_account record (actually only hashes of branchNodes can be saved)
@see UnmarshalTrieNode
@see integrity.Trie

+-----------------------------------------------------------------------------------------------------+
| DB record: 0x0B, hasState: 0b1011, hasTree: 0b1001, hasHash: 0b1001, hashes: [x,x]                  |
+-----------------------------------------------------------------------------------------------------+

	|                                           |                               |
	v                                           |                               v

+---------------------------------------------+             |            +--------------------------------------+
| DB record: 0x0B00, hasState: 0b10001        |             |            | DB record: 0x0B03, hasState: 0b10010 |
| hasTree: 0, hasHash: 0b10000, hashes: [x]   |             |            | hasTree: 0, hasHash: 0, hashes: []   |
+---------------------------------------------+             |            +--------------------------------------+

	|                    |                              |                         |                  |
	v                    v                              v                         v                  v

+------------------+    +----------------------+     +---------------+        +---------------+  +---------------+
| Account:         |    | BranchNode: 0x0B0004 |     | Account:      |        | Account:      |  | Account:      |
| 0x0B0000...      |    | has no record in     |     | 0x0B01...     |        | 0x0B0301...   |  | 0x0B0304...   |
| in HashedAccount |    |     TrieAccount      |     |               |        |               |  |               |
+------------------+    +----------------------+     +---------------+        +---------------+  +---------------+

	                           |                |
	                           v                v
			           +---------------+  +---------------+
			           | Account:      |  | Account:      |
			           | 0x0B000400... |  | 0x0B000401... |
			           +---------------+  +---------------+

Invariants:
- hasTree is subset of hasState
- hasHash is subset of hasState
- first level in account_trie always exists if hasState>0
- TrieStorage record of account.root (length=40) must have +1 hash - it's account.root
- each record in TrieAccount table must have parent (may be not direct) and this parent must have correct bit in hasTree bitmap
- if hasState has bit - then HashedAccount table must have record according to this bit
- each TrieAccount record must cover some state (means hasState is always > 0)
- TrieAccount records with length=1 can satisfy (hasBranch==0&&hasHash==0) condition
- Other records in TrieAccount and TrieStorage must (hasTree!=0 || hasHash!=0)
*/
const TrieOfAccounts = "TrieAccount"
const TrieOfStorage = "TrieStorage"

const (
	// DatabaseInfo is used to store information about data layout.
	DatabaseInfo = "DbInfo"

	// Data item prefixes (use single byte to avoid mixing data types, avoid `i`, used for indexes).
	HeaderNumber = "HeaderNumber" // header_hash -> num_u64

	HeaderCanonical = "CanonicalHeader"        // block_num_u64 -> header hash
	Headers         = "Header"                 // block_num_u64 + hash -> header (RLP)
	HeaderTD        = "HeadersTotalDifficulty" // block_num_u64 + hash -> td (RLP)

	BlockBody = "BlockBody" // block_num_u64 + hash -> block body

	// EthTx - stores only txs of canonical blocks. As a result - id's used in this table are also
	// canonical - same across all nodex in network - regardless reorgs. Transactions of
	// non-canonical blocs are not removed, but moved to NonCanonicalTransaction - then during re-org don't
	// need re-download block from network.
	// Also this table has system-txs before and after block: if
	// block has no system-tx - records are absent, but sequence increasing
	EthTx           = "BlockTransaction"        // tbl_sequence_u64 -> rlp(tx)
	NonCanonicalTxs = "NonCanonicalTransaction" // tbl_sequence_u64 -> rlp(tx)

	Receipts = "Receipt"        // block_num_u64 -> canonical block receipts (non-canonical are not stored)
	Log      = "TransactionLog" // block_num_u64 + txId -> logs of transaction

	// Stores bitmap indices - in which block numbers saw logs of given 'address' or 'topic'
	// [addr or topic] + [2 bytes inverted shard number] -> bitmap(blockN)
	// indices are sharded - because some bitmaps are >1Mb and when new incoming blocks process it
	//	 updates ~300 of bitmaps - by append small amount new values. It cause much big writes (MDBX does copy-on-write).
	//
	// if last existing shard size merge it with delta
	// if serialized size of delta > ShardLimit - break down to multiple shards
	// shard number - it's biggest value in bitmap
	LogTopicIndex   = "LogTopicIndex"
	LogAddressIndex = "LogAddressIndex"

	// CallTraceSet is the name of the table that contain the mapping of block number to the set (sorted) of all accounts
	// touched by call traces. It is DupSort-ed table
	// 8-byte BE block number -> account address -> two bits (one for "from", another for "to")
	CallTraceSet = "CallTraceSet"
	// Indices for call traces - have the same format as LogTopicIndex and LogAddressIndex
	// Store bitmap indices - in which block number we saw calls from (CallFromIndex) or to (CallToIndex) some addresses
	CallFromIndex = "CallFromIndex"
	CallToIndex   = "CallToIndex"

	// Cumulative indexes for estimation of stage execution
	CumulativeGasIndex         = "CumulativeGasIndex"
	CumulativeTransactionIndex = "CumulativeTransactionIndex"

	TxLookup = "BlockTransactionLookup" // hash -> transaction/receipt lookup metadata

	ConfigTable = "Config" // blockchain prefix for the db

	// Progress of sync stages: stageName -> stageData
	SyncStageProgress = "SyncStage"

	Clique             = "Clique"
	CliqueSeparate     = "CliqueSeparate"
	CliqueSnapshot     = "CliqueSnapshot"
	CliqueLastSnapshot = "CliqueLastSnapshot"

	// Snapshot table used for Binance Smart Chain's consensus engine Parlia
	// Schema of key/value pairs containing:
	// Key (string): SnapshotFullKey = SnapshotBucket + num (uint64 big endian) + hash
	// Value (JSON blob):
	// {
	//     "number"             // Block number where the snapshot was created
	//     "hash"               // Block hash where the snapshot was created
	//     "validators"         // Set of authorized validators at this moment
	//     "recents"            // Set of recent validators for spam protections
	//     "recent_fork_hashes" // Set of recent forkHash
	// }
	ParliaSnapshot = "ParliaSnapshot"

	// Proof-of-stake
	// Beacon chain head that is been executed at the current time
	CurrentExecutionPayload = "CurrentExecutionPayload"

	// Node database tables (see nodedb.go)

	// NodeRecords stores P2P node records (ENR)
	NodeRecords = "NodeRecord"
	// Inodes stores P2P discovery service info about the nodes
	Inodes = "Inode"

	// Transaction senders - stored separately from the block bodies
	Senders = "TxSender" // block_num_u64 + blockHash -> sendersList (no serialization format, every 20 bytes is new sender)

	// headBlockKey tracks the latest know full block's hash.
	HeadBlockKey = "LastBlock"

	HeadHeaderKey = "LastHeader"

	// headBlockHash, safeBlockHash, finalizedBlockHash of the latest Engine API forkchoice
	LastForkchoice = "LastForkchoice"

	// TransitionBlockKey tracks the last proof-of-work block
	TransitionBlockKey = "TransitionBlock"

	// migrationName -> serialized SyncStageProgress and SyncStageUnwind buckets
	// it stores stages progress to understand in which context was executed migration
	// in case of bug-report developer can ask content of this bucket
	Migrations = "Migration"

	Sequence = "Sequence" // tbl_name -> seq_u64

	Epoch        = "DevEpoch"        // block_num_u64+block_hash->transition_proof
	PendingEpoch = "DevPendingEpoch" // block_num_u64+block_hash->transition_proof

	Issuance = "Issuance" // block_num_u64->RLP(issuance+burnt[0 if < london])

	StateAccounts   = "StateAccounts"
	StateStorage    = "StateStorage"
	StateCode       = "StateCode"
	StateCommitment = "StateCommitment"

	// BOR

	BorReceipts = "BorReceipt"
	BorTxLookup = "BlockBorTransactionLookup"
	BorSeparate = "BorSeparate"

	// Downloader
	BittorrentCompletion = "BittorrentCompletion"
	BittorrentInfo       = "BittorrentInfo"

	// Domains and Inverted Indices
	AccountKeys        = "AccountKeys"
	AccountVals        = "AccountVals"
	AccountHistoryKeys = "AccountHistoryKeys"
	AccountHistoryVals = "AccountHistoryVals"
	AccountSettings    = "AccountSettings"
	AccountIdx         = "AccountIdx"

	StorageKeys        = "StorageKeys"
	StorageVals        = "StorageVals"
	StorageHistoryKeys = "StorageHistoryKeys"
	StorageHistoryVals = "StorageHistoryVals"
	StorageSettings    = "StorageSettings"
	StorageIdx         = "StorageIdx"

	CodeKeys        = "CodeKeys"
	CodeVals        = "CodeVals"
	CodeHistoryKeys = "CodeHistoryKeys"
	CodeHistoryVals = "CodeHistoryVals"
	CodeSettings    = "CodeSettings"
	CodeIdx         = "CodeIdx"

	LogAddressKeys = "LogAddressKeys"
	LogAddressIdx  = "LogAddressIdx"
	LogTopicsKeys  = "LogTopicsKeys"
	LogTopicsIdx   = "LogTopicsIdx"

	TracesFromKeys = "TracesFromKeys"
	TracesFromIdx  = "TracesFromIdx"
	TracesToKeys   = "TracesToKeys"
	TracesToIdx    = "TracesToIdx"

	Snapshots = "Snapshots" // name -> hash

	RAccountKeys = "RAccountKeys"
	RAccountIdx  = "RAccountIdx"
	RStorageKeys = "RStorageKeys"
	RStorageIdx  = "RStorageIdx"
	RCodeKeys    = "RCodeKeys"
	RCodeIdx     = "RCodeIdx"

	PlainStateR    = "PlainStateR"    // temporary table for PlainState reconstitution
	CodeR          = "CodeR"          // temporary table for Code reconstitution
	PlainContractR = "PlainContractR" // temporary table for PlainContract reconstitution

	XAccount = "XAccount"
	XStorage = "XStorage"
	XCode    = "XCode"
)

// Keys
var (
	//StorageModeTEVM - does not translate EVM to TEVM
	StorageModeTEVM = []byte("smTEVM")

	PruneTypeOlder  = []byte("older")
	PruneTypeBefore = []byte("before")

	PruneHistory        = []byte("pruneHistory")
	PruneHistoryType    = []byte("pruneHistoryType")
	PruneReceipts       = []byte("pruneReceipts")
	PruneReceiptsType   = []byte("pruneReceiptsType")
	PruneTxIndex        = []byte("pruneTxIndex")
	PruneTxIndexType    = []byte("pruneTxIndexType")
	PruneCallTraces     = []byte("pruneCallTraces")
	PruneCallTracesType = []byte("pruneCallTracesType")

	DBSchemaVersionKey = []byte("dbVersion")

	BittorrentPeerID            = "peerID"
	CurrentHeadersSnapshotHash  = []byte("CurrentHeadersSnapshotHash")
	CurrentHeadersSnapshotBlock = []byte("CurrentHeadersSnapshotBlock")
	CurrentBodiesSnapshotHash   = []byte("CurrentBodiesSnapshotHash")
	CurrentBodiesSnapshotBlock  = []byte("CurrentBodiesSnapshotBlock")
)

// ChaindataTables - list of all buckets. App will panic if some bucket is not in this list.
// This list will be sorted in `init` method.
// ChaindataTablesCfg - can be used to find index in sorted version of ChaindataTables list by name
var ChaindataTables = []string{
	AccountsHistory,
	StorageHistory,
	Code,
	ContractCode,
	HeaderNumber,
	BlockBody,
	Receipts,
	TxLookup,
	ConfigTable,
	CurrentExecutionPayload,
	DatabaseInfo,
	IncarnationMap,
	ContractTEVMCode,
	CliqueSeparate,
	CliqueLastSnapshot,
	CliqueSnapshot,
	ParliaSnapshot,
	SyncStageProgress,
	PlainState,
	PlainContractCode,
	AccountChangeSet,
	StorageChangeSet,
	Senders,
	HeadBlockKey,
	HeadHeaderKey,
	LastForkchoice,
	Migrations,
	LogTopicIndex,
	LogAddressIndex,
	CallTraceSet,
	CallFromIndex,
	CallToIndex,
	CumulativeGasIndex,
	CumulativeTransactionIndex,
	Log,
	Sequence,
	EthTx,
	NonCanonicalTxs,
	TrieOfAccounts,
	TrieOfStorage,
	HashedAccounts,
	HashedStorage,
	HeaderCanonical,
	Headers,
	HeaderTD,
	Epoch,
	PendingEpoch,
	Issuance,
	StateAccounts,
	StateStorage,
	StateCode,
	StateCommitment,
	BorReceipts,
	BorTxLookup,
	BorSeparate,
	AccountKeys,
	AccountVals,
	AccountHistoryKeys,
	AccountHistoryVals,
	AccountSettings,
	AccountIdx,

	StorageKeys,
	StorageVals,
	StorageHistoryKeys,
	StorageHistoryVals,
	StorageSettings,
	StorageIdx,

	CodeKeys,
	CodeVals,
	CodeHistoryKeys,
	CodeHistoryVals,
	CodeSettings,
	CodeIdx,

	LogAddressKeys,
	LogAddressIdx,
	LogTopicsKeys,
	LogTopicsIdx,

	TracesFromKeys,
	TracesFromIdx,
	TracesToKeys,
	TracesToIdx,

	Snapshots,

	RAccountKeys,
	RAccountIdx,
	RStorageKeys,
	RStorageIdx,
	RCodeKeys,
	RCodeIdx,
}

const (
	RecentLocalTransaction = "RecentLocalTransaction" // sequence_u64 -> tx_hash
	PoolTransaction        = "PoolTransaction"        // txHash -> sender_id_u64+tx_rlp
	PoolInfo               = "PoolInfo"               // option_key -> option_value
)

var TxPoolTables = []string{
	RecentLocalTransaction,
	PoolTransaction,
	PoolInfo,
}
var SentryTables = []string{}
var DownloaderTables = []string{
	BittorrentCompletion,
	BittorrentInfo,
}
var ReconTables = []string{
	XAccount,
	XStorage,
	XCode,
	PlainStateR,
	CodeR,
	PlainContractR,
}

// ChaindataDeprecatedTables - list of buckets which can be programmatically deleted - for example after migration
var ChaindataDeprecatedTables = []string{
	Clique,
	TransitionBlockKey,
}

type CmpFunc func(k1, k2, v1, v2 []byte) int

type TableCfg map[string]TableCfgItem
type Bucket string

type DBI uint
type TableFlags uint

const (
	Default    TableFlags = 0x00
	ReverseKey TableFlags = 0x02
	DupSort    TableFlags = 0x04
	IntegerKey TableFlags = 0x08
	IntegerDup TableFlags = 0x20
	ReverseDup TableFlags = 0x40
)

type TableCfgItem struct {
	Flags TableFlags
	// AutoDupSortKeysConversion - enables some keys transformation - to change db layout without changing app code.
	// Use it wisely - it helps to do experiments with DB format faster, but better reduce amount of Magic in app.
	// If good DB format found, push app code to accept this format and then disable this property.
	AutoDupSortKeysConversion bool
	IsDeprecated              bool
	DBI                       DBI
	// DupFromLen - if user provide key of this length, then next transformation applied:
	// v = append(k[DupToLen:], v...)
	// k = k[:DupToLen]
	// And opposite at retrieval
	// Works only if AutoDupSortKeysConversion enabled
	DupFromLen int
	DupToLen   int
}

var ChaindataTablesCfg = TableCfg{
	HashedStorage: {
		Flags:                     DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                72,
		DupToLen:                  40,
	},
	AccountChangeSet: {Flags: DupSort},
	StorageChangeSet: {Flags: DupSort},
	PlainState: {
		Flags:                     DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                60,
		DupToLen:                  28,
	},
	CallTraceSet: {Flags: DupSort},

	AccountKeys:        {Flags: DupSort},
	AccountHistoryKeys: {Flags: DupSort},
	AccountIdx:         {Flags: DupSort},
	StorageKeys:        {Flags: DupSort},
	StorageHistoryKeys: {Flags: DupSort},
	StorageIdx:         {Flags: DupSort},
	CodeKeys:           {Flags: DupSort},
	CodeHistoryKeys:    {Flags: DupSort},
	CodeIdx:            {Flags: DupSort},
	LogAddressKeys:     {Flags: DupSort},
	LogAddressIdx:      {Flags: DupSort},
	LogTopicsKeys:      {Flags: DupSort},
	LogTopicsIdx:       {Flags: DupSort},
	TracesFromKeys:     {Flags: DupSort},
	TracesFromIdx:      {Flags: DupSort},
	TracesToKeys:       {Flags: DupSort},
	TracesToIdx:        {Flags: DupSort},
	RAccountKeys:       {Flags: DupSort},
	RAccountIdx:        {Flags: DupSort},
	RStorageKeys:       {Flags: DupSort},
	RStorageIdx:        {Flags: DupSort},
	RCodeKeys:          {Flags: DupSort},
	RCodeIdx:           {Flags: DupSort},
}

var TxpoolTablesCfg = TableCfg{}
var SentryTablesCfg = TableCfg{}
var DownloaderTablesCfg = TableCfg{}
var ReconTablesCfg = TableCfg{}

func sortBuckets() {
	sort.SliceStable(ChaindataTables, func(i, j int) bool {
		return strings.Compare(ChaindataTables[i], ChaindataTables[j]) < 0
	})
}

func init() {
	reinit()
}

func reinit() {
	sortBuckets()

	for _, name := range ChaindataTables {
		_, ok := ChaindataTablesCfg[name]
		if !ok {
			ChaindataTablesCfg[name] = TableCfgItem{}
		}
	}

	for _, name := range ChaindataDeprecatedTables {
		_, ok := ChaindataTablesCfg[name]
		if !ok {
			ChaindataTablesCfg[name] = TableCfgItem{}
		}
		tmp := ChaindataTablesCfg[name]
		tmp.IsDeprecated = true
		ChaindataTablesCfg[name] = tmp
	}

	for _, name := range TxPoolTables {
		_, ok := TxpoolTablesCfg[name]
		if !ok {
			TxpoolTablesCfg[name] = TableCfgItem{}
		}
	}

	for _, name := range SentryTables {
		_, ok := SentryTablesCfg[name]
		if !ok {
			SentryTablesCfg[name] = TableCfgItem{}
		}
	}

	for _, name := range DownloaderTables {
		_, ok := DownloaderTablesCfg[name]
		if !ok {
			DownloaderTablesCfg[name] = TableCfgItem{}
		}
	}

	for _, name := range ReconTables {
		_, ok := ReconTablesCfg[name]
		if !ok {
			ReconTablesCfg[name] = TableCfgItem{}
		}
	}
}