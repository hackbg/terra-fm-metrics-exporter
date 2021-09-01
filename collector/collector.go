package collector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"

	"github.com/hackbg/terra-chainlink-exporter/types"
	"github.com/rs/zerolog/log"
	"github.com/segmentio/kafka-go"
	tmrTypes "github.com/tendermint/tendermint/abci/types"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	tmTypes "github.com/tendermint/tendermint/types"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

type Collector struct {
	TendermintClient *tmrpc.HTTP
	WasmClient       wasmTypes.QueryClient
}

var KAFKA_SERVER = os.Getenv("KAFKA_SERVER")

func newKafkaWriter(kafkaUrl, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:     kafka.TCP(kafkaUrl),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
}

func NewCollector(grpcConn *grpc.ClientConn, TendermintRpc string) Collector {

	client, err := tmrpc.New(TendermintRpc, "/websocket")
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create Tendermint Client")
	}

	err = client.Start()
	if err != nil {
		fmt.Println(err)
	}

	// Kafka
	writer := newKafkaWriter(KAFKA_SERVER, "Terra")

	handler := func(event types.EventRecords) {
		for _, round := range event.NewRound {
			res, err := json.Marshal(round)
			if err != nil {
				continue
			}
			err = writer.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("NewRound"),
					Value: res,
				},
			)
			if err != nil {
				fmt.Println(err)
			}
			fmt.Println("WRITTEN TO NEW ROUND")
		}
		for _, round := range event.SubmissionReceived {
			res, err := json.Marshal(round)
			if err != nil {
				continue
			}
			err = writer.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("Submission Received"),
					Value: res,
				},
			)
			if err != nil {
				fmt.Println(err)
			}
			fmt.Println("WRITTEN SUBMISSION")
		}
		for _, update := range event.AnswerUpdated {
			res, err := json.Marshal(update)
			if err != nil {
				continue
			}
			err = writer.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("Answer Updated"),
					Value: res,
				},
			)
			if err != nil {
				fmt.Println(err)
			}
			fmt.Println("WRITTEN SUBMISSION")
		}
	}
	queryParams := `tm.event='Tx' AND execute_contract.contract_address='terra1tndcaqxkpc5ce9qee5ggqf430mr2z3pefe5wj6'`

	if err != nil {
		panic(err)
	}

	//err = wsConn.Subscribe(context.Background(), "subscribe", "unsubscribe", params, responses)
	out, err := client.Subscribe(context.Background(), "sub", queryParams)
	if err != nil {
		panic(err)
	}

	go func() {
		for {
			resp, ok := <-out
			if !ok {
				return
			}
			info := extractTxInfo(resp.Data.(tmTypes.EventDataTx))
			if err != nil {
				continue
			}
			eventRecords, err := parseEvents(resp.Data.(tmTypes.EventDataTx).Result.Events, info)
			if err != nil {
				continue
			}
			if eventRecords != nil {
				handler(*eventRecords)
			}
		}
	}()

	return Collector{
		TendermintClient: client,
		WasmClient:       wasmTypes.NewQueryClient(grpcConn),
	}
}

// Queries
func (collector Collector) GetLatestBlockHeight() int64 {
	status, err := collector.TendermintClient.Status(context.Background())
	if err != nil {
		log.Fatal().Err(err).Msg("Could not query Tendermint status")
	}

	log.Info().Str("network", status.NodeInfo.Network).Msg("Got network status from Tendermint")

	latestHeight := status.SyncInfo.LatestBlockHeight
	return latestHeight
}

func (collector Collector) GetLatestRoundData(aggregatorAddress string) (*wasmTypes.QueryContractStoreResponse, error) {
	response, err := collector.WasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: aggregatorAddress,
			QueryMsg:        []byte(`{"get_latest_round_data": {}}`),
		},
	)

	return response, err
}

func (collector Collector) GetAggregatorConfig(aggregatorAddress string) (*wasmTypes.QueryContractStoreResponse, error) {
	response, err := collector.WasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: aggregatorAddress,
			QueryMsg:        []byte(`{"get_aggregator_config": {}}`),
		},
	)

	return response, err
}

// Events
func extractTxInfo(data tmTypes.EventDataTx) types.TxInfo {
	h := sha256.Sum256(data.Tx)
	var txInfo = types.TxInfo{
		Height: data.Height,
		Tx:     fmt.Sprintf("%x", h[:]),
	}
	return txInfo
}

func parseEvents(events []tmrTypes.Event, txInfo types.TxInfo) (*types.EventRecords, error) {
	var eventRecords types.EventRecords
	for _, event := range events {
		switch event.Type {
		case "wasm-new_round":
			newRound, err := parseNewRoundEvent(event)
			newRound.Height = txInfo.Height
			newRound.TxHash = txInfo.Tx
			if err != nil {
				return nil, err
			}
			eventRecords.NewRound = append(eventRecords.NewRound, *newRound)
		case "wasm-submission_received":
			submission, err := parseSubmissionReceivedEvent(event)
			submission.Height = txInfo.Height
			submission.TxHash = txInfo.Tx
			if err != nil {
				return nil, err
			}
			eventRecords.SubmissionReceived = append(eventRecords.SubmissionReceived, *submission)
		case "wasm-answer_updated":
			answerUpdated, err := parseAnswerUpdatedEvent(event)
			answerUpdated.Height = txInfo.Height
			answerUpdated.TxHash = txInfo.Tx
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
	submission := new(big.Int)
	submission, _ = submission.SetString(attributes["submission"], 10)
	return &types.EventSubmissionReceived{
		Sender:     types.Addr(attributes["oracle"]),
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
