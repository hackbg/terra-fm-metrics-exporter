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
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

const FluxAggregator = "terra1tndcaqxkpc5ce9qee5ggqf430mr2z3pefe5wj6"

var (
	ChainId       string
	ConstLabels   map[string]string
	rpcAddr       = os.Getenv("RPC")
	TendermintRpc = "http://localhost:26657"
)

var log = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

type LatestRoundReponse struct {
	RoundId         string
	Answer          string
	StartedAt       string
	UpdatedAt       string
	AnsweredInRound string
}

func IntToString(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		fmt.Println(err)
	}
	return i
}

func TerraChainlinkHandler(w http.ResponseWriter, r *http.Request, grpcConn *grpc.ClientConn) {
	sublogger := log.With().
		Str("request-id", uuid.New().String()).
		Logger()

	fmLatestRoundResponseGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "flux_monitor_answers",
			Help:        "Reports the current on chain price for a feed.",
			ConstLabels: ConstLabels,
		}, []string{"contract_address"},
	)

	registry := prometheus.NewRegistry()
	registry.MustRegister(fmLatestRoundResponseGauge)
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

		var res LatestRoundReponse
		err = json.Unmarshal(response.QueryResult, &res)

		if err != nil {
			sublogger.Error().Err(err).Msg("Could not parse response")
			return
		}

		fmt.Println(response)

		fmLatestRoundResponseGauge.With(prometheus.Labels{
			"contract_address": FluxAggregator,
		}).Set(float64(IntToString(res.Answer)))
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

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		TerraChainlinkHandler(w, r, grpcConn)
	})
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not start application")
	}
}
