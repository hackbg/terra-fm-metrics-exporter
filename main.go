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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

const FluxAggregator = "terra1tndcaqxkpc5ce9qee5ggqf430mr2z3pefe5wj6"

var (
	ChainId       string
	ConstLabels   map[string]string
	rpcAddr       = os.Getenv("TERRA_RPC")
	TendermintRpc = os.Getenv("TENDERMINT_RPC")
)

var log = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

type LatestRoundResponse struct {
	RoundId         int    `json:"round_id"`
	Answer          string `json:"answer"`
	StartedAt       int    `json:"started_at"`
	UpdatedAt       int    `json:"updated_at"`
	AnsweredInRound int    `json:"answered_in_round"`
}

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
		"chain_id": ChainId,
	}
}

func TerraChainlinkHandler(w http.ResponseWriter, r *http.Request, grpcConn *grpc.ClientConn) {
	sublogger := log.With().
		Str("request-id", uuid.New().String()).
		Logger()

	nodeMetadataGauge := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name:        "node_metadata",
			Help:        "Exposes metdata for node",
			ConstLabels: ConstLabels,
		},
	)

	fmAnswersTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_answers_total",
			Help: "Reports the number of on-chain answers for a feed",
		},
		[]string{"contract_address"},
	)

	fmLatestRoundResponseGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_answers",
			Help: "Reports the current on chain price for a feed.",
		},
		[]string{"contract_address"},
	)

	registry := prometheus.NewRegistry()
	registry.MustRegister(fmLatestRoundResponseGauge)
	registry.MustRegister(nodeMetadataGauge)
	registry.MustRegister(fmAnswersTotal)

	var wg sync.WaitGroup

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Started querying flux monitor")
		wasmClient := wasmTypes.NewQueryClient(grpcConn)
		response, err := wasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: FluxAggregator,
				QueryMsg:        []byte(`{"get_latest_round_data": {}}`),
			},
		)
		if err != nil {
			sublogger.Error().Err(err).Msg("Could not query the contract")
			return
		}

		var res LatestRoundResponse
		err = json.Unmarshal(response.QueryResult, &res)

		if err != nil {
			sublogger.Error().Err(err).Msg("Could not parse response")
			return
		}

		fmt.Println(res)

		fmLatestRoundResponseGauge.With(prometheus.Labels{
			"contract_address": FluxAggregator,
		}).Set(float64(StringToInt(res.Answer)))

		fmAnswersTotal.With(prometheus.Labels{
			"contract_address": FluxAggregator,
		}).Add(float64(res.RoundId))

		nodeMetadataGauge.Set(0)
	}()
	wg.Add(1)
	wg.Wait()

	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	h.ServeHTTP(w, r)
}

func main() {
	fmt.Println(rpcAddr)
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
