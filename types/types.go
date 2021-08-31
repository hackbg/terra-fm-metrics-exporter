package types

import "math/big"

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
	RoundId   uint32 `json:"RoundId"`
	StartedBy Addr   `json:"StartedBy"`
	StartedAt uint64 `json:"StartedAt"`
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
	Value   Value
	RoundId uint32
}

type EventSubmissionReceived struct {
	Submission Value
	RoundId    uint32
	Oracle     Addr
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
