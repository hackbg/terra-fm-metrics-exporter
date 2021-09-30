package collector

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/hackbg/terra-chainlink-exporter/types"
	tmrTypes "github.com/tendermint/tendermint/abci/types"
	tmTypes "github.com/tendermint/tendermint/types"
)

func ExtractTxInfo(data tmTypes.EventDataTx) types.TxInfo {
	h := sha256.Sum256(data.Tx)
	txInfo := types.TxInfo{
		Height: data.Height,
		Tx:     fmt.Sprintf("%x", h[:]),
	}
	return txInfo
}

func ParseEvents(events []tmrTypes.Event, txInfo types.TxInfo, feedAddr string) (*types.EventRecords, error) {
	var eventRecords types.EventRecords
	for _, event := range events {
		switch event.Type {
		case "wasm-new_round":
			newRound, err := parseNewRoundEvent(event)
			newRound.Height = txInfo.Height
			newRound.TxHash = txInfo.Tx
			newRound.Feed = feedAddr
			if err != nil {
				return nil, err
			}
			eventRecords.NewRound = append(eventRecords.NewRound, *newRound)
		case "wasm-submission_received":
			submission, err := parseSubmissionReceivedEvent(event)
			submission.Height = txInfo.Height
			submission.TxHash = txInfo.Tx
			submission.Feed = feedAddr
			if err != nil {
				return nil, err
			}
			eventRecords.SubmissionReceived = append(eventRecords.SubmissionReceived, *submission)
		case "wasm-answer_updated":
			answerUpdated, err := parseAnswerUpdatedEvent(event)
			answerUpdated.Height = txInfo.Height
			answerUpdated.TxHash = txInfo.Tx
			answerUpdated.Feed = feedAddr
			if err != nil {
				return nil, err
			}
			eventRecords.AnswerUpdated = append(eventRecords.AnswerUpdated, *answerUpdated)
		case "wasm-oracle_permissions_updated":
			permissionsUpdated, err := parseOraclePermissionsUpdatedEvent(event)
			if err != nil {
				return nil, err
			}
			eventRecords.OraclePermissionsUpdated = append(eventRecords.OraclePermissionsUpdated, permissionsUpdated...)
		case "wasm-confirm_aggregator":
			fmt.Println("Got wasm-confirm-aggregator")
			confirmAggregator, err := parseConfirmAggregatorEvent(event)
			if err != nil {
				return nil, err
			}

			eventRecords.ConfirmAggregator = append(eventRecords.ConfirmAggregator, *confirmAggregator)
		}
	}

	return &eventRecords, nil
}

func parseNewRoundEvent(event tmrTypes.Event) (*types.EventNewRound, error) {
	attributes, err := getRequiredAttributes(event, []string{"round_id", "started_by"})
	if err != nil {
		return nil, err
	}
	roundId, err := strconv.Atoi(attributes["round_id"])
	if err != nil {
		return nil, err
	}

	return &types.EventNewRound{
		RoundId: uint32(roundId),
	}, nil
}

func parseSubmissionReceivedEvent(event tmrTypes.Event) (*types.EventSubmissionReceived, error) {
	attributes, err := getRequiredAttributes(event, []string{"submission", "round_id", "oracle"})
	if err != nil {
		return nil, err
	}

	roundId, err := strconv.Atoi(attributes["round_id"])
	if err != nil {
		return nil, err
	}

	submission := new(big.Int)
	submission, _ = submission.SetString(attributes["submission"], 10)
	return &types.EventSubmissionReceived{
		Sender:     types.Addr(attributes["oracle"]),
		RoundId:    uint32(roundId),
		Submission: types.Value{Key: *submission},
	}, nil
}

func parseAnswerUpdatedEvent(event tmrTypes.Event) (*types.EventAnswerUpdated, error) {
	attributes, err := getRequiredAttributes(event, []string{"round_id", "current"})
	if err != nil {
		return nil, err
	}

	roundId, err := strconv.Atoi(attributes["round_id"])
	if err != nil {
		return nil, err
	}

	value := new(big.Int)
	value, _ = value.SetString(attributes["current"], 10)

	return &types.EventAnswerUpdated{
		Value:   types.Value{Key: *value},
		RoundId: uint32(roundId),
	}, nil
}

func parseOraclePermissionsUpdatedEvent(event tmrTypes.Event) (events []types.EventOraclePermissionsUpdated, err error) {
	attributes, err := getRequiredAttributes(event, []string{"added", "removed"})
	if err != nil {
		return nil, err
	}

	var added []string
	err = json.Unmarshal([]byte(attributes["added"]), &added)
	if err != nil {
		return nil, err
	}
	for _, oracle := range added {
		events = append(events, types.EventOraclePermissionsUpdated{
			Oracle: types.Addr(oracle),
			Bool:   true,
		})
	}

	var removed []string
	err = json.Unmarshal([]byte(attributes["removed"]), &removed)
	if err != nil {
		return nil, err
	}
	for _, oracle := range removed {
		events = append(events, types.EventOraclePermissionsUpdated{
			Oracle: types.Addr(oracle),
			Bool:   false,
		})
	}

	return
}

func parseConfirmAggregatorEvent(event tmrTypes.Event) (*types.EventConfirmAggregator, error) {
	attributes, err := getRequiredAttributes(event, []string{"contract_address"})

	if err != nil {
		return nil, err
	}

	address := attributes["contract_address"]

	return &types.EventConfirmAggregator{
		NewAggregator: address,
	}, nil
}

func getAttributeValue(event tmrTypes.Event, attributeKey string) (string, error) {
	for _, attr := range event.Attributes {
		if string(attr.Key) == attributeKey {
			return string(attr.Value), nil
		}
	}

	return "", fmt.Errorf("attribute key %s does not exist", attributeKey)
}

func getRequiredAttributes(event tmrTypes.Event, attributes []string) (map[string]string, error) {
	var attrs = make(map[string]string)
	for _, attr := range attributes {
		value, err := getAttributeValue(event, attr)
		if err != nil {
			return nil, err
		}

		attrs[attr] = value
	}
	return attrs, nil
}
