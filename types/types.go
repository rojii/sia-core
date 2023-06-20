// Package types defines the essential types of the Sia blockchain.
package types

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"lukechampine.com/frand"
)

const (
	// MaxRevisionNumber is used to finalize a FileContract. When a contract's
	// RevisionNumber is set to this value, no further revisions are possible.
	MaxRevisionNumber = math.MaxUint64

	// RenterContractIndex defines the index of the renter's output and public
	// key in a FileContract.
	RenterContractIndex = 0

	// HostContractIndex defines the index of the host's output and public key in
	// a FileContract.
	HostContractIndex = 1
)

// Various specifiers.
var (
	SpecifierEd25519       = NewSpecifier("ed25519")
	SpecifierSiacoinOutput = NewSpecifier("siacoin output")
	SpecifierSiafundOutput = NewSpecifier("siafund output")
	SpecifierFileContract  = NewSpecifier("file contract")
	SpecifierStorageProof  = NewSpecifier("storage proof")
	SpecifierFoundation    = NewSpecifier("foundation")
	SpecifierEntropy       = NewSpecifier("entropy")
)

// A Hash256 is a generic 256-bit cryptographic hash.
type Hash256 [32]byte

// A 32 byte hash of the Mainchains BMM Block.
type BMMHash [32]byte

// A PublicKey is an Ed25519 public key.
type PublicKey [32]byte

// VerifyHash verifies that s is a valid signature of h by pk.
func (pk PublicKey) VerifyHash(h Hash256, s Signature) bool {
	return ed25519.Verify(pk[:], h[:], s[:])
}

// UnlockKey returns pk as an UnlockKey.
func (pk PublicKey) UnlockKey() UnlockKey {
	return UnlockKey{
		Algorithm: SpecifierEd25519,
		Key:       pk[:],
	}
}

// StandardAddress returns the standard address derived from pk.
func (pk PublicKey) StandardAddress() Address {
	return standardUnlockHash(pk)
}

// StandardUnlockConditions returns the standard unlock conditions for pk.
func (pk PublicKey) StandardUnlockConditions() UnlockConditions {
	return UnlockConditions{
		PublicKeys:         []UnlockKey{pk.UnlockKey()},
		SignaturesRequired: 1,
	}
}

// A PrivateKey is an Ed25519 private key.
type PrivateKey []byte

// PublicKey returns the PublicKey corresponding to priv.
func (priv PrivateKey) PublicKey() (pk PublicKey) {
	copy(pk[:], priv[32:])
	return
}

// SignHash signs h with priv, producing a Signature.
func (priv PrivateKey) SignHash(h Hash256) (s Signature) {
	copy(s[:], ed25519.Sign(ed25519.PrivateKey(priv), h[:]))
	return
}

// NewPrivateKeyFromSeed calculates a private key from a seed.
func NewPrivateKeyFromSeed(seed []byte) PrivateKey {
	return PrivateKey(ed25519.NewKeyFromSeed(seed))
}

// GeneratePrivateKey creates a new private key from a secure entropy source.
func GeneratePrivateKey() PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	frand.Read(seed)
	pk := NewPrivateKeyFromSeed(seed)
	for i := range seed {
		seed[i] = 0
	}
	return pk
}

// A Signature is an Ed25519 signature.
type Signature [64]byte

// A Specifier is a fixed-size, 0-padded identifier.
type Specifier [16]byte

// NewSpecifier returns a specifier containing the provided name.
func NewSpecifier(name string) (s Specifier) {
	copy(s[:], name)
	return
}

// An UnlockKey can provide one of the signatures required by a set of
// UnlockConditions.
type UnlockKey struct {
	Algorithm Specifier
	Key       []byte
}

// UnlockConditions specify the conditions for spending an output or revising a
// file contract.
type UnlockConditions struct {
	Timelock           uint64      `json:"timelock"`
	PublicKeys         []UnlockKey `json:"publicKeys"`
	SignaturesRequired uint64      `json:"signaturesRequired"`
}

// UnlockHash computes the hash of a set of UnlockConditions. Such hashes are
// most commonly used as addresses, but are also used in file contracts.
func (uc UnlockConditions) UnlockHash() Address {
	// almost all UnlockConditions are standard, so optimize for that case
	if uc.Timelock == 0 &&
		len(uc.PublicKeys) == 1 &&
		uc.PublicKeys[0].Algorithm == SpecifierEd25519 &&
		len(uc.PublicKeys[0].Key) == len(PublicKey{}) &&
		uc.SignaturesRequired == 1 {
		return standardUnlockHash(*(*PublicKey)(uc.PublicKeys[0].Key))
	}

	h := hasherPool.Get().(*Hasher)
	defer hasherPool.Put(h)
	h.Reset()

	var acc merkleAccumulator
	h.E.WriteUint8(leafHashPrefix)
	h.E.WriteUint64(uc.Timelock)
	acc.addLeaf(h.Sum())
	for _, key := range uc.PublicKeys {
		h.Reset()
		h.E.WriteUint8(leafHashPrefix)
		key.EncodeTo(h.E)
		acc.addLeaf(h.Sum())
	}
	h.Reset()
	h.E.WriteUint8(leafHashPrefix)
	h.E.WriteUint64(uc.SignaturesRequired)
	acc.addLeaf(h.Sum())
	return Address(acc.root())
}

// An Address is the hash of a set of UnlockConditions.
type Address Hash256

// VoidAddress is an address whose signing key does not exist. Sending coins to
// this address ensures that they will never be recoverable by anyone.
var VoidAddress Address

// A BlockID uniquely identifies a block.
type BlockID Hash256

// CmpWork compares the work implied by two BlockIDs.
func (bid BlockID) CmpWork(t BlockID) int {
	// work is the inverse of the ID, so reverse the comparison
	return bytes.Compare(t[:], bid[:])
}

// MinerOutputID returns the ID of the block's i'th miner payout.
func (bid BlockID) MinerOutputID(i int) SiacoinOutputID {
	buf := make([]byte, 32+8)
	copy(buf, bid[:])
	binary.LittleEndian.PutUint64(buf[32:], uint64(i))
	return SiacoinOutputID(HashBytes(buf))
}

// FoundationOutputID returns the ID of the block's Foundation subsidy.
func (bid BlockID) FoundationOutputID() SiacoinOutputID {
	buf := make([]byte, 32+16)
	copy(buf, bid[:])
	copy(buf[32:], SpecifierFoundation[:])
	return SiacoinOutputID(HashBytes(buf))
}

// A TransactionID uniquely identifies a transaction.
type TransactionID Hash256

// A ChainIndex pairs a block's height with its ID.
type ChainIndex struct {
	Height uint64  `json:"height"`
	ID     BlockID `json:"ID"`
}

// A SiacoinOutput is the recipient of some of the siacoins spent in a
// transaction.
type SiacoinOutput struct {
	Value   Currency `json:"value"`
	Address Address  `json:"address"`
}

// A SiacoinOutputID uniquely identifies a siacoin output.
type SiacoinOutputID Hash256

// A SiacoinInput spends an unspent SiacoinOutput in the UTXO set by
// revealing and satisfying its unlock conditions.
type SiacoinInput struct {
	ParentID         SiacoinOutputID  `json:"parentID"`
	UnlockConditions UnlockConditions `json:"unlockConditions"`
}

// A SiafundOutput is the recipient of some of the siafunds spent in a
// transaction.
type SiafundOutput struct {
	Value   uint64  `json:"value"`
	Address Address `json:"address"`
}

// A SiafundOutputID uniquely identifies a siafund output.
type SiafundOutputID Hash256

// ClaimOutputID returns the ID of the SiacoinOutput that is created when
// the siafund output is spent.
func (sfoid SiafundOutputID) ClaimOutputID() SiacoinOutputID {
	return SiacoinOutputID(HashBytes(sfoid[:]))
}

// A SiafundInput spends an unspent SiafundOutput in the UTXO set by revealing
// and satisfying its unlock conditions. SiafundInputs also include a
// ClaimAddress, specifying the recipient of the siacoins that were earned by
// the output.
type SiafundInput struct {
	ParentID         SiafundOutputID  `json:"parentID"`
	UnlockConditions UnlockConditions `json:"unlockConditions"`
	ClaimAddress     Address          `json:"claimAddress"`
}

// A FileContract is a storage agreement between a renter and a host. It
// contains a bidirectional payment channel that resolves as either "valid" or
// "missed" depending on whether a valid StorageProof is submitted for the
// contract.
type FileContract struct {
	Filesize           uint64          `json:"filesize"`
	FileMerkleRoot     Hash256         `json:"fileMerkleRoot"`
	WindowStart        uint64          `json:"windowStart"`
	WindowEnd          uint64          `json:"windowEnd"`
	Payout             Currency        `json:"payout"`
	ValidProofOutputs  []SiacoinOutput `json:"validProofOutputs"`
	MissedProofOutputs []SiacoinOutput `json:"missedProofOutputs"`
	UnlockHash         Hash256         `json:"unlockHash"`
	RevisionNumber     uint64          `json:"revisionNumber"`
}

// EndHeight returns the height at which the contract's host is no longer
// obligated to store the contract data.
func (fc *FileContract) EndHeight() uint64 { return fc.WindowStart }

// ValidRenterOutput returns the output that will be created for the renter if
// the contract resolves valid.
func (fc *FileContract) ValidRenterOutput() SiacoinOutput {
	return fc.ValidProofOutputs[RenterContractIndex]
}

// ValidRenterPayout returns the amount of siacoins that the renter will receive
// if the contract resolves valid.
func (fc *FileContract) ValidRenterPayout() Currency { return fc.ValidRenterOutput().Value }

// MissedRenterOutput returns the output that will be created for the renter if
// the contract resolves missed.
func (fc *FileContract) MissedRenterOutput() SiacoinOutput {
	return fc.MissedProofOutputs[RenterContractIndex]
}

// MissedRenterPayout returns the amount of siacoins that the renter will receive
// if the contract resolves missed.
func (fc *FileContract) MissedRenterPayout() Currency { return fc.MissedRenterOutput().Value }

// ValidHostOutput returns the output that will be created for the host if
// the contract resolves valid.
func (fc *FileContract) ValidHostOutput() SiacoinOutput {
	return fc.ValidProofOutputs[HostContractIndex]
}

// ValidHostPayout returns the amount of siacoins that the host will receive
// if the contract resolves valid.
func (fc *FileContract) ValidHostPayout() Currency { return fc.ValidHostOutput().Value }

// MissedHostOutput returns the output that will be created for the host if
// the contract resolves missed.
func (fc *FileContract) MissedHostOutput() SiacoinOutput {
	return fc.MissedProofOutputs[HostContractIndex]
}

// MissedHostPayout returns the amount of siacoins that the host will receive
// if the contract resolves missed.
func (fc *FileContract) MissedHostPayout() Currency { return fc.MissedHostOutput().Value }

// A FileContractID uniquely identifies a file contract.
type FileContractID Hash256

// ValidOutputID returns the ID of the valid proof output at index i.
func (fcid FileContractID) ValidOutputID(i int) SiacoinOutputID {
	h := hasherPool.Get().(*Hasher)
	defer hasherPool.Put(h)
	h.Reset()
	SpecifierStorageProof.EncodeTo(h.E)
	fcid.EncodeTo(h.E)
	h.E.WriteBool(true)
	h.E.WriteUint64(uint64(i))
	return SiacoinOutputID(h.Sum())
}

// MissedOutputID returns the ID of the missed proof output at index i.
func (fcid FileContractID) MissedOutputID(i int) SiacoinOutputID {
	h := hasherPool.Get().(*Hasher)
	defer hasherPool.Put(h)
	h.Reset()
	SpecifierStorageProof.EncodeTo(h.E)
	fcid.EncodeTo(h.E)
	h.E.WriteBool(false)
	h.E.WriteUint64(uint64(i))
	return SiacoinOutputID(h.Sum())
}

// A FileContractRevision updates the state of an existing file contract.
type FileContractRevision struct {
	ParentID         FileContractID   `json:"parentID"`
	UnlockConditions UnlockConditions `json:"unlockConditions"`
	// NOTE: the Payout field of the contract is not "really" part of a
	// revision. A revision cannot change the total payout, so the original siad
	// code defines FileContractRevision as an entirely separate struct without
	// a Payout field. Here, we instead reuse the FileContract type, which means
	// we must treat its Payout field as invalid. To guard against developer
	// error, we set it to a sentinel value when decoding it.
	FileContract
}

// A StorageProof asserts the presence of a randomly-selected leaf within the
// Merkle tree of a FileContract's data.
type StorageProof struct {
	ParentID FileContractID
	Leaf     [64]byte
	Proof    []Hash256
}

// A FoundationAddressUpdate updates the primary and failsafe Foundation subsidy
// addresses.
type FoundationAddressUpdate struct {
	NewPrimary  Address `json:"newPrimary"`
	NewFailsafe Address `json:"newFailsafe"`
}

// CoveredFields indicates which fields of a transaction are covered by a
// signature.
type CoveredFields struct {
	WholeTransaction      bool     `json:"wholeTransaction,omitempty"`
	SiacoinInputs         []uint64 `json:"siacoinInputs,omitempty"`
	SiacoinOutputs        []uint64 `json:"siacoinOutputs,omitempty"`
	FileContracts         []uint64 `json:"fileContracts,omitempty"`
	FileContractRevisions []uint64 `json:"fileContractRevisions,omitempty"`
	StorageProofs         []uint64 `json:"storageProofs,omitempty"`
	SiafundInputs         []uint64 `json:"siafundInputs,omitempty"`
	SiafundOutputs        []uint64 `json:"siafundOutputs,omitempty"`
	MinerFees             []uint64 `json:"minerFees,omitempty"`
	ArbitraryData         []uint64 `json:"arbitraryData,omitempty"`
	Signatures            []uint64 `json:"signatures,omitempty"`
}

// A TransactionSignature signs transaction data.
type TransactionSignature struct {
	ParentID       Hash256       `json:"parentID"`
	PublicKeyIndex uint64        `json:"publicKeyIndex"`
	Timelock       uint64        `json:"timelock,omitempty"`
	CoveredFields  CoveredFields `json:"coveredFields"`
	Signature      []byte        `json:"signature"`
}

// A Transaction transfers value by consuming existing Outputs and creating new
// Outputs.
type Transaction struct {
	SiacoinInputs         []SiacoinInput         `json:"siacoinInputs,omitempty"`
	SiacoinOutputs        []SiacoinOutput        `json:"siacoinOutputs,omitempty"`
	FileContracts         []FileContract         `json:"fileContracts,omitempty"`
	FileContractRevisions []FileContractRevision `json:"fileContractRevisions,omitempty"`
	StorageProofs         []StorageProof         `json:"storageProofs,omitempty"`
	SiafundInputs         []SiafundInput         `json:"siafundInputs,omitempty"`
	SiafundOutputs        []SiafundOutput        `json:"siafundOutputs,omitempty"`
	MinerFees             []Currency             `json:"minerFees,omitempty"`
	ArbitraryData         [][]byte               `json:"arbitraryData,omitempty"`
	Signatures            []TransactionSignature `json:"signatures,omitempty"`
}

// ID returns the "semantic hash" of the transaction, covering all of the
// transaction's effects, but not incidental data such as signatures. This
// ensures that the ID will remain stable (i.e. non-malleable).
//
// To hash all of the data in a transaction, use the EncodeTo method.
func (txn *Transaction) ID() TransactionID {
	h := hasherPool.Get().(*Hasher)
	defer hasherPool.Put(h)
	h.Reset()
	txn.encodeNoSignatures(h.E)
	return TransactionID(h.Sum())
}

// SiacoinOutputID returns the ID of the siacoin output at index i.
func (txn *Transaction) SiacoinOutputID(i int) SiacoinOutputID {
	h := hasherPool.Get().(*Hasher)
	defer hasherPool.Put(h)
	h.Reset()
	SpecifierSiacoinOutput.EncodeTo(h.E)
	txn.encodeNoSignatures(h.E)
	h.E.WriteUint64(uint64(i))
	return SiacoinOutputID(h.Sum())
}

// SiafundOutputID returns the ID of the siafund output at index i.
func (txn *Transaction) SiafundOutputID(i int) SiafundOutputID {
	h := hasherPool.Get().(*Hasher)
	defer hasherPool.Put(h)
	h.Reset()
	SpecifierSiafundOutput.EncodeTo(h.E)
	txn.encodeNoSignatures(h.E)
	h.E.WriteUint64(uint64(i))
	return SiafundOutputID(h.Sum())
}

// SiafundClaimOutputID returns the ID of the siacoin claim output for the
// siafund input at index i.
func (txn *Transaction) SiafundClaimOutputID(i int) SiacoinOutputID {
	sfid := txn.SiafundOutputID(i)
	return SiacoinOutputID(HashBytes(sfid[:]))
}

// FileContractID returns the ID of the file contract at index i.
func (txn *Transaction) FileContractID(i int) FileContractID {
	h := hasherPool.Get().(*Hasher)
	defer hasherPool.Put(h)
	h.Reset()
	SpecifierFileContract.EncodeTo(h.E)
	txn.encodeNoSignatures(h.E)
	h.E.WriteUint64(uint64(i))
	return FileContractID(h.Sum())
}

// A BlockHeader contains a Block's non-transaction data.
type BlockHeader struct {
	ParentID      BlockID   `json:"parentID"`
	Nonce         uint64    `json:"nonce"`
	Timestamp     time.Time `json:"timestamp"`
	MerkleRoot    Hash256   `json:"merkleRoot"`
	PrevMainBlock Hash256   `json:"prevmainblock"`
}

// ID returns a hash that uniquely identifies a block.
func (bh BlockHeader) ID() BlockID {
	buf := make([]byte, 32+8+8+32)
	copy(buf[0:32], bh.ParentID[:])
	binary.LittleEndian.PutUint64(buf[32:40], bh.Nonce)
	binary.LittleEndian.PutUint64(buf[40:48], uint64(bh.Timestamp.Unix()))
	copy(buf[48:80], bh.MerkleRoot[:])
	return BlockID(HashBytes(buf))
}

// CurrentTimestamp returns the current time, rounded to the nearest second. The
// time zone is set to UTC.
func CurrentTimestamp() time.Time { return time.Now().Round(time.Second).UTC() }

// A Block is a set of transactions grouped under a header.
type Block struct {
	ParentID     BlockID         `json:"parentID"`
	Nonce        uint64          `json:"nonce"`
	Timestamp    time.Time       `json:"timestamp"`
	MinerPayouts []SiacoinOutput `json:"minerPayouts"`
	Transactions []Transaction   `json:"transactions"`
}

// Header returns the header for the block.
//
// Note that this is a relatively expensive operation, as it computes the Merkle
// root of the block's transactions.
func (b *Block) Header() BlockHeader {
	h := hasherPool.Get().(*Hasher)
	defer hasherPool.Put(h)
	var acc merkleAccumulator
	for _, mp := range b.MinerPayouts {
		h.Reset()
		h.E.WriteUint8(leafHashPrefix)
		mp.EncodeTo(h.E)
		acc.addLeaf(h.Sum())
	}
	for _, txn := range b.Transactions {
		h.Reset()
		h.E.WriteUint8(leafHashPrefix)
		txn.EncodeTo(h.E)
		acc.addLeaf(h.Sum())
	}
	return BlockHeader{
		ParentID:   b.ParentID,
		Nonce:      b.Nonce,
		Timestamp:  b.Timestamp,
		MerkleRoot: acc.root(),
	}
}

// ID returns a hash that uniquely identifies a block. It is equivalent to
// b.Header().ID().
//
// Note that this is a relatively expensive operation, as it computes the Merkle
// root of the block's transactions.
func (b *Block) ID() BlockID { return b.Header().ID() }

// Implementations of fmt.Stringer, encoding.Text(Un)marshaler, and json.(Un)marshaler

func stringerHex(prefix string, data []byte) string {
	return prefix + ":" + hex.EncodeToString(data[:])
}

func marshalHex(prefix string, data []byte) ([]byte, error) {
	return []byte(stringerHex(prefix, data)), nil
}

func unmarshalHex(dst []byte, prefix string, data []byte) error {
	n, err := hex.Decode(dst, bytes.TrimPrefix(data, []byte(prefix+":")))
	if n < len(dst) {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return fmt.Errorf("decoding %v:<hex> failed: %w", prefix, err)
	}
	return nil
}

// String implements fmt.Stringer.
func (h Hash256) String() string { return stringerHex("h", h[:]) }

// MarshalText implements encoding.TextMarshaler.
func (h Hash256) MarshalText() ([]byte, error) { return marshalHex("h", h[:]) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (h *Hash256) UnmarshalText(b []byte) error { return unmarshalHex(h[:], "h", b) }

// String implements fmt.Stringer.
func (ci ChainIndex) String() string {
	// use the 4 least-significant bytes of ID -- in a mature chain, the
	// most-significant bytes will be zeros
	return fmt.Sprintf("%d::%x", ci.Height, ci.ID[len(ci.ID)-4:])
}

// MarshalText implements encoding.TextMarshaler.
func (ci ChainIndex) MarshalText() ([]byte, error) {
	return []byte(fmt.Sprintf("%d::%x", ci.Height, ci.ID[:])), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (ci *ChainIndex) UnmarshalText(b []byte) (err error) {
	parts := bytes.Split(b, []byte("::"))
	if len(parts) != 2 {
		return fmt.Errorf("decoding <height>::<id> failed: wrong number of separators")
	} else if ci.Height, err = strconv.ParseUint(string(parts[0]), 10, 64); err != nil {
		return fmt.Errorf("decoding <height>::<id> failed: %w", err)
	} else if n, err := hex.Decode(ci.ID[:], parts[1]); err != nil {
		return fmt.Errorf("decoding <height>::<id> failed: %w", err)
	} else if n < len(ci.ID) {
		return fmt.Errorf("decoding <height>::<id> failed: %w", io.ErrUnexpectedEOF)
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (ci ChainIndex) MarshalJSON() ([]byte, error) {
	type jsonCI ChainIndex // hide MarshalText method
	return json.Marshal(jsonCI(ci))
}

// UnmarshalJSON implements json.Unmarshaler.
func (ci *ChainIndex) UnmarshalJSON(b []byte) error {
	type jsonCI ChainIndex // hide UnmarshalText method
	return json.Unmarshal(b, (*jsonCI)(ci))
}

// ParseChainIndex parses a chain index from a string.
func ParseChainIndex(s string) (ci ChainIndex, err error) {
	err = ci.UnmarshalText([]byte(s))
	return
}

// String implements fmt.Stringer.
func (s Specifier) String() string { return string(bytes.Trim(s[:], "\x00")) }

// MarshalText implements encoding.TextMarshaler.
func (s Specifier) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Specifier) UnmarshalText(b []byte) error {
	str, err := strconv.Unquote(string(b))
	if err != nil {
		str = string(b)
	}
	if len(str) > len(s) {
		return fmt.Errorf("specifier %s too long", str)
	}
	copy(s[:], str)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (uk UnlockKey) MarshalText() ([]byte, error) {
	return marshalHex(uk.Algorithm.String(), uk.Key[:])
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (uk *UnlockKey) UnmarshalText(b []byte) error {
	parts := bytes.Split(b, []byte(":"))
	if len(parts) != 2 {
		return fmt.Errorf("decoding <algorithm>::<key> failed: wrong number of separators")
	} else if err := uk.Algorithm.UnmarshalText(parts[0]); err != nil {
		return fmt.Errorf("decoding <algorithm>::<key> failed: %w", err)
	} else if uk.Key, err = hex.DecodeString(string(parts[1])); err != nil {
		return fmt.Errorf("decoding <algorithm>::<key> failed: %w", err)
	}
	return nil
}

// String implements fmt.Stringer.
func (a Address) String() string {
	checksum := HashBytes(a[:])
	return stringerHex("addr", append(a[:], checksum[:6]...))
}

// MarshalText implements encoding.TextMarshaler.
func (a Address) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (a *Address) UnmarshalText(b []byte) (err error) {
	withChecksum := make([]byte, 32+6)
	n, err := hex.Decode(withChecksum, bytes.TrimPrefix(b, []byte("addr:")))
	if err != nil {
		err = fmt.Errorf("decoding addr:<hex> failed: %w", err)
	} else if n != len(withChecksum) {
		err = fmt.Errorf("decoding addr:<hex> failed: %w", io.ErrUnexpectedEOF)
	} else if checksum := HashBytes(withChecksum[:32]); !bytes.Equal(checksum[:6], withChecksum[32:]) {
		err = errors.New("bad checksum")
	}
	copy(a[:], withChecksum[:32])
	return
}

// ParseAddress parses an address from a prefixed hex encoded string.
func ParseAddress(s string) (a Address, err error) {
	err = a.UnmarshalText([]byte(s))
	return
}

// String implements fmt.Stringer.
func (bid BlockID) String() string { return stringerHex("bid", bid[:]) }

// MarshalText implements encoding.TextMarshaler.
func (bid BlockID) MarshalText() ([]byte, error) { return marshalHex("bid", bid[:]) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (bid *BlockID) UnmarshalText(b []byte) error { return unmarshalHex(bid[:], "bid", b) }

// String implements fmt.Stringer.
func (pk PublicKey) String() string { return stringerHex("ed25519", pk[:]) }

// MarshalText implements encoding.TextMarshaler.
func (pk PublicKey) MarshalText() ([]byte, error) { return marshalHex("ed25519", pk[:]) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (pk *PublicKey) UnmarshalText(b []byte) error { return unmarshalHex(pk[:], "ed25519", b) }

// String implements fmt.Stringer.
func (tid TransactionID) String() string { return stringerHex("txid", tid[:]) }

// MarshalText implements encoding.TextMarshaler.
func (tid TransactionID) MarshalText() ([]byte, error) { return marshalHex("txid", tid[:]) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (tid *TransactionID) UnmarshalText(b []byte) error { return unmarshalHex(tid[:], "txid", b) }

// String implements fmt.Stringer.
func (scoid SiacoinOutputID) String() string { return stringerHex("scoid", scoid[:]) }

// MarshalText implements encoding.TextMarshaler.
func (scoid SiacoinOutputID) MarshalText() ([]byte, error) { return marshalHex("scoid", scoid[:]) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (scoid *SiacoinOutputID) UnmarshalText(b []byte) error {
	return unmarshalHex(scoid[:], "scoid", b)
}

// String implements fmt.Stringer.
func (sfoid SiafundOutputID) String() string { return stringerHex("sfoid", sfoid[:]) }

// MarshalText implements encoding.TextMarshaler.
func (sfoid SiafundOutputID) MarshalText() ([]byte, error) { return marshalHex("sfoid", sfoid[:]) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (sfoid *SiafundOutputID) UnmarshalText(b []byte) error {
	return unmarshalHex(sfoid[:], "sfoid", b)
}

// String implements fmt.Stringer.
func (fcid FileContractID) String() string { return stringerHex("fcid", fcid[:]) }

// MarshalText implements encoding.TextMarshaler.
func (fcid FileContractID) MarshalText() ([]byte, error) { return marshalHex("fcid", fcid[:]) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (fcid *FileContractID) UnmarshalText(b []byte) error { return unmarshalHex(fcid[:], "fcid", b) }

// String implements fmt.Stringer.
func (sig Signature) String() string { return stringerHex("sig", sig[:]) }

// MarshalText implements encoding.TextMarshaler.
func (sig Signature) MarshalText() ([]byte, error) { return marshalHex("sig", sig[:]) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (sig *Signature) UnmarshalText(b []byte) error { return unmarshalHex(sig[:], "sig", b) }

// MarshalJSON implements json.Marshaler.
func (sp StorageProof) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ParentID FileContractID `json:"parentID"`
		Leaf     string         `json:"leaf"`
		Proof    []Hash256      `json:"proof"`
	}{sp.ParentID, hex.EncodeToString(sp.Leaf[:]), sp.Proof})
}

// UnmarshalJSON implements json.Marshaler.
func (sp *StorageProof) UnmarshalJSON(b []byte) error {
	var leaf string
	err := json.Unmarshal(b, &struct {
		ParentID *FileContractID
		Leaf     *string
		Proof    *[]Hash256
	}{&sp.ParentID, &leaf, &sp.Proof})
	if err != nil {
		return err
	} else if len(leaf) != len(sp.Leaf)*2 {
		return errors.New("invalid storage proof leaf length")
	} else if _, err = hex.Decode(sp.Leaf[:], []byte(leaf)); err != nil {
		return err
	}
	return nil
}
