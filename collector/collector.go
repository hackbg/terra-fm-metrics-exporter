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
	tmrTypes "github.com/tendermint/tendermint/abci/types"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	tmTypes "github.com/tendermint/tendermint/types"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

var (
	RPC_ADDR       = os.Getenv("TERRA_RPC")
	TENDERMINT_RPC = os.Getenv("TENDERMINT_RPC")
)

type Collector struct {
	TendermintClient *tmrpc.HTTP
	WasmClient       wasmTypes.QueryClient
}

func (c *Collector) Subscribe(ctx context.Context, method string, params string) (out <-chan ctypes.ResultEvent, err error) {
	return c.TendermintClient.Subscribe(ctx, method, params)
}

func (c *Collector) Unsubscribe(ctx context.Context, method string, params string) error {
	return c.TendermintClient.Unsubscribe(ctx, method, params)
}

func NewCollector(client tmrpc.HTTP) (*Collector, error) {

	grpcConn, err := grpc.Dial(
		RPC_ADDR,
		grpc.WithInsecure(),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not establish a grpc connection")
	}

	return &Collector{
		TendermintClient: &client,
		WasmClient:       wasmTypes.NewQueryClient(grpcConn),
	}, nil
}

// Queries
func GetLatestBlockHeight(c Collector) int64 {
	status, err := c.TendermintClient.Status(context.Background())
	if err != nil {
		log.Error().Err(err).Msg("Could not query Tendermint status")
	}

	log.Info().Str("network", status.NodeInfo.Network).Msg("Got network status from Tendermint")

	latestHeight := status.SyncInfo.LatestBlockHeight
	return latestHeight
}

func (c Collector) GetLatestRoundData(aggregatorAddress string) (*wasmTypes.QueryContractStoreResponse, error) {
	response, err := c.WasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: aggregatorAddress,
			QueryMsg:        []byte(`{"aggregator_query": {"get_latest_round_data": {}}}`),
		},
	)

	if err != nil {
		log.Error().Err(err)
	}

	return response, err
}

func (c Collector) GetAggregatorConfig(aggregatorAddress string) (*wasmTypes.QueryContractStoreResponse, error) {
	response, err := c.WasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: aggregatorAddress,
			QueryMsg:        []byte(`{"get_aggregator": {}}`),
		},
	)

	if err != nil {
		log.Error().Err(err)
		return nil, err
	}

	var addr string

	err = json.Unmarshal(response.QueryResult, &addr)

	if err != nil {
		log.Error().Err(err)
	}

	config, err := c.WasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: addr,
			QueryMsg:        []byte(`{"get_aggregator_config": {}}`),
		},
	)

	return config, err
}

func (c Collector) GetAggregator(proxyAddress string) (address *string, err error) {
	response, err := c.WasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: proxyAddress,
			QueryMsg:        []byte(`{"get_aggregator": {}}`),
		},
	)

	if err != nil {
		log.Error().Err(err)
		return nil, err
	}

	var addr string
	err = json.Unmarshal(response.QueryResult, &addr)

	if err != nil {
		log.Error().Err(err)
		return nil, err
	}

	return &addr, nil
}

// Events
func ExtractTxInfo(data tmTypes.EventDataTx) types.TxInfo {
	h := sha256.Sum256(data.Tx)
	var txInfo = types.TxInfo{
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

	var address string = attributes["contract_address"]

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
