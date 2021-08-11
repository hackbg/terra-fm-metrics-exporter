package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/hackbg/terra-chainlink-exporter/subscriber"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	tmrTypes "github.com/tendermint/tendermint/abci/types"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"github.com/tidwall/gjson"
	"google.golang.org/grpc"
)

var aggregators = []string{"terra1tndcaqxkpc5ce9qee5ggqf430mr2z3pefe5wj6"}

var (
	ChainId       string
	ContractType  = "flux_monitor"
	ConstLabels   map[string]string
	WsUrl         = os.Getenv("WS_URL")
	rpcAddr       = os.Getenv("TERRA_RPC")
	TendermintRpc = os.Getenv("TENDERMINT_RPC")
)

var log = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

type (
	AggregatorConfigResponse struct {
		Link               string `json:"link,omitempty"`
		Validator          string `json:"validator,omitempty"`
		PaymentAmount      string `json:"payment_amount,omitempty"`
		MaxSubmissionCount int    `json:"max_submission_count,omitempty"`
		MinSubmissionCount int    `json:"min_submission_count,omitempty"`
		RestartDelay       int    `json:"restart_delay,omitempty"`
		Timeout            int    `json:"timeout,omitempty"`
		Decimals           int    `json:"decimal,omitempty"`
		Description        string `json:"description,omitempty"`
		MinSubmissionValue string `json:"min_submission_value,omitempty"`
		MaxSubmissionValue string `json:"max_submission_value,omitempty"`
	}
	LatestRoundResponse struct {
		RoundId         int    `json:"round_id"`
		Answer          string `json:"answer"`
		StartedAt       int    `json:"started_at"`
		UpdatedAt       int    `json:"updated_at"`
		AnsweredInRound int    `json:"answered_in_round"`
	}
)

func StringToInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		fmt.Println(err)
	}
	return i
}

func setChainId() {
	client, err := tmrpc.New(TendermintRpc, "/websocket")
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create Tendermint client")
	}

	status, err := client.Status(context.Background())
	if err != nil {
		log.Fatal().Err(err).Msg("Could not query Tendermint status")
	}

	log.Info().Str("network", status.NodeInfo.Network).Msg("Got network status from Tendermint")
	ChainId = status.NodeInfo.Network
	ConstLabels = map[string]string{
		"chain_id":      ChainId,
		"contract_type": ContractType,
	}
}

func getLatestBlockHeight() int64 {
	client, err := tmrpc.New(TendermintRpc, "/websocket")
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create Tendermint client")
	}

	status, err := client.Status(context.Background())
	if err != nil {
		log.Fatal().Err(err).Msg("Could not query Tendermint status")
	}

	log.Info().Str("network", status.NodeInfo.Network).Msg("Got network status from Tendermint")

	latestHeight := status.SyncInfo.LatestBlockHeight
	return latestHeight
}

func getLatestRoundData(wasmClient wasmTypes.QueryClient, aggregatorAddress string) (*wasmTypes.QueryContractStoreResponse, error) {
	response, err := wasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: aggregatorAddress,
			QueryMsg:        []byte(`{"get_latest_round_data": {}}`),
		},
	)

	return response, err
}

func getAggregatorConfig(wasmClient wasmTypes.QueryClient, aggregatorAddress string) (*wasmTypes.QueryContractStoreResponse, error) {
	response, err := wasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: aggregatorAddress,
			QueryMsg:        []byte(`{"get_aggregator_config": {}}`),
		},
	)

	return response, err
}

func TerraChainlinkHandler(w http.ResponseWriter, r *http.Request, grpcConn *grpc.ClientConn) {
	sublogger := log.With().
		Str("request-id", uuid.New().String()).
		Logger()

	// Gauges

	nodeMetadataGauge := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name:        "node_metadata",
			Help:        "Exposes metdata for node",
			ConstLabels: ConstLabels,
		},
	)

	// TODO
	feedContractMetadataGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "feed_contract_metadata",
			Help: "Exposes metadata for individual feeds",
		},
		[]string{"chain_id", "contract_address", "contract_status", "contract_type", "feed_name", "feed_path", "network_id", "network_name", "symbol"},
	)

	fmAnswersGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_answers",
			Help: "Reports the current on chain price for a feed.",
		},
		[]string{"contract_address"},
	)

	fmCurrentHeadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "head_tracker_current_head",
			Help: "Tracks the current block height that the monitoring instance has processed",
		},
		[]string{"network_name", "chain_id", "network_id"},
	)

	// TODO
	fmSubmissionReceivedValuesGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_submission_received_values",
			Help: "Reports the current submission value for an oracle on a feed",
		},
		[]string{"contract_address", "sender"},
	)

	// TODO: should be doable?
	fmLatestRoundResponsesGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_latest_round_responses",
			Help: "Reports the current number of submissions received for the latest round for a feed",
		},
		[]string{"contract_address"},
	)

	// Counters
	// Should be possible?
	fmAnswersTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_answers_total",
			Help: "Reports the number of on-chain answers for a feed",
		},
		[]string{"contract_address"},
	)

	// TODO, maybe not possible
	fmSubmissionsReceivedTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_submissions_received_total",
			Help: "Reports the current number of submissions",
		},
		[]string{"contract_address", "sender"},
	)

	// Should be okay
	fmRoundsCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_rounds",
			Help: "The number of rounds the monitor has observed on a feed",
		},
		[]string{},
	)

	// Register metrics
	registry := prometheus.NewRegistry()
	registry.MustRegister(fmAnswersGauge)
	registry.MustRegister(fmLatestRoundResponsesGauge)
	registry.MustRegister(nodeMetadataGauge)
	registry.MustRegister(fmAnswersTotal)
	registry.MustRegister(fmCurrentHeadGauge)
	registry.MustRegister(feedContractMetadataGauge)
	registry.MustRegister(fmSubmissionReceivedValuesGauge)
	registry.MustRegister(fmSubmissionsReceivedTotal)
	registry.MustRegister(fmRoundsCounter)

	var wg sync.WaitGroup

	wasmClient := wasmTypes.NewQueryClient(grpcConn)

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Querying the node information")
		height := getLatestBlockHeight()
		fmCurrentHeadGauge.With(prometheus.Labels{
			"chain_id": ChainId,
			// TODO
			"network_name": "",
			"network_id":   "",
		}).Set(float64(height))
	}()
	wg.Add(1)

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Started querying aggregator answers")

		for aggregator := range aggregators {
			response, err := getLatestRoundData(wasmClient, aggregators[aggregator])

			if err != nil {
				sublogger.Error().Err(err).Msg("Could not query the contract")
				return
			}

			var res LatestRoundResponse
			err = json.Unmarshal(response.QueryResult, &res)

			if err != nil {
				sublogger.Error().Err(err).Msg("Could not parse the response")
				return
			}

			fmAnswersGauge.With(prometheus.Labels{
				"contract_address": aggregators[aggregator],
			}).Set(float64(StringToInt(res.Answer)))

			fmAnswersTotal.With(prometheus.Labels{
				"contract_address": aggregators[aggregator],
			}).Add(float64(res.RoundId))
		}
	}()
	wg.Add(1)

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Querying aggregators metadata")

		for aggregator := range aggregators {
			response, err := getAggregatorConfig(wasmClient, aggregators[aggregator])

			if err != nil {
				sublogger.Error().Err(err).Msg("Could not query the contract")
				return
			}

			var res AggregatorConfigResponse
			err = json.Unmarshal(response.QueryResult, &res)

			if err != nil {
				sublogger.Error().Err(err).Msg("Could not parse the response")
				return
			}

			feedContractMetadataGauge.With(prometheus.Labels{
				"chain_id":         ChainId,
				"contract_address": aggregators[aggregator],
				"contract_status":  "active",
				"contract_type":    ContractType,
				"feed_name":        res.Description,
				"feed_path":        "",
				"network_id":       "",
				"network_name":     "",
				"symbol":           "",
			}).Set(1)
		}
	}()
	wg.Add(1)
	wg.Wait()

	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	h.ServeHTTP(w, r)
}

func extractEvents(data json.RawMessage) ([]tmrTypes.Event, error) {
	value := gjson.Get(string(data), "data.value.TxResult.result.events")

	var events []tmrTypes.Event
	err := json.Unmarshal([]byte(value.Raw), &events)
	if err != nil {
		return nil, err
	}

	return events, nil
}

func main() {
	fmt.Println(rpcAddr)
	conn, err := subscriber.NewSubscriber("ws://" + WsUrl + "/websocket")
	if err != nil {
		panic(err)
	}
	responses := make(chan json.RawMessage)
	queryParams := `tm.event='Tx'`
	filter := []string{queryParams}

	params, err := json.Marshal(filter)
	if err != nil {
		panic(err)
	}

	err = conn.Subscribe(context.Background(), "subscribe", "unsubscribe", params, responses)
	if err != nil {
		panic(err)
	}

	go func() {
		for {
			resp, ok := <-responses
			if !ok {
				return
			}

			response, err := extractEvents(resp)
			if err != nil {
				panic(err)
			}

			fmt.Println(response)
		}
	}()

	grpcConn, err := grpc.Dial(
		rpcAddr,
		grpc.WithInsecure(),
	)

	if err != nil {
		panic(err)
	}
	defer grpcConn.Close()

	setChainId()

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		TerraChainlinkHandler(w, r, grpcConn)
	})
	err = http.ListenAndServe(":8089", nil)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not start application")
	}
}
