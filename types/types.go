package types

import (
	"math/big"
)

type Value struct {
	Key big.Int
}

func (v *Value) UnmarshalJSON(data []byte) error {
	var i big.Int
	*v = Value{Key: *i.SetBytes(data)}

	return nil
}

func (b Value) MarshalJSON() ([]byte, error) {
	return []byte(b.Key.String()), nil
}

type Addr string

type option struct {
	hasValue bool
}

func (o option) IsNone() bool {
	return !o.hasValue
}

func (o option) IsSome() bool {
	return o.hasValue
}

type OptionBigInt struct {
	option
	Value
}

type OptionU32 struct {
	option
	uint32
}

type OptionU64 struct {
	option
	uint64
}

type OptionAddr struct {
	option
	Addr
}

// Events
type EventNewRound struct {
	Height  int64  `json:"block_number"`
	TxHash  string `json:"tx_hash"`
	RoundId uint32 `json:"RoundId"`
}

type EventRoundDetailsUpdated struct {
	PaymentAmount  Value `json:"value"`
	MinSubmissions uint32
	MaxSubmissions uint32
	RestartDelay   uint32
	Timeout        uint32
}

type EventOraclePermissionsUpdated struct {
	Oracle Addr
	Bool   bool
}

type EventAnswerUpdated struct {
	Height  int64  `json:"block_number"`
	TxHash  string `json:"tx_hash"`
	Value   Value  `json:"current_answer"`
	RoundId uint32 `json:"round_id"`
}

type EventSubmissionReceived struct {
	Height     int64  `json:"block_number"`
	TxHash     string `json:"tx_hash"`
	Submission Value  `json:"submission"`
	RoundId    uint32 `json:"round_id"`
	Sender     Addr   `json:"sender"`
}

type QueryResponse struct {
	Height string
	Result interface{}
}

type EventRecords struct {
	NewRound                 []EventNewRound
	RoundDetailsUpdated      []EventRoundDetailsUpdated
	OraclePermissionsUpdated []EventOraclePermissionsUpdated
	AnswerUpdated            []EventAnswerUpdated
	SubmissionReceived       []EventSubmissionReceived
}

type TxInfo struct {
	Height int64
	Tx     string
}

type Feed struct {
	ContractAddress string   `json:"contractAddress"`
	ContractVersion int      `json:"contractVersion"`
	DecimalPlaces   int      `json:"decimalPlaces"`
	Heartbeat       int64    `json:"heartbeat"`
	History         bool     `json:"history"`
	Multiply        string   `json:"multiply"`
	Name            string   `json:"name"`
	Symbol          string   `json:"symbol"`
	Pair            []string `json:"pair"`
	Path            string   `json:"path"`
	NodeCount       int      `json:"nodeCount"`
	Status          string   `json:"status"`
}

type LatestRoundInfo struct {
	RoundId     uint32 `json:"round_id"`
	Submissions uint32 `json:"submissions"`
}
