package types

import "math/big"

type Value struct {
	big.Int
}

func (v *Value) UnmarshalJSON(data []byte) error {
	var i big.Int
	*v = Value{*i.SetBytes(data)}

	return nil
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
	RoundId   uint32
	StartedBy Addr
	StartedAt uint64
}

type EventRoundDetailsUpdated struct {
	PaymentAmount  Value
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
	// FluxMonitor requests
	NewRound                 []EventNewRound
	RoundDetailsUpdated      []EventRoundDetailsUpdated
	OraclePermissionsUpdated []EventOraclePermissionsUpdated
	AnswerUpdated            []EventAnswerUpdated
	SubmissionReceived       []EventSubmissionReceived
}
