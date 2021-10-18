package exporter

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/go-cmp/cmp"
	"github.com/hackbg/terra-chainlink-exporter/collector"
	"github.com/hackbg/terra-chainlink-exporter/manager"
	"github.com/hackbg/terra-chainlink-exporter/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	tmTypes "github.com/tendermint/tendermint/types"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

var (
	FEED_CONFIG_URL       = os.Getenv("FEED_CONFIG_URL")
	NODE_CONFIG_URL       = os.Getenv("NODE_CONFIG_URL")
	RPC_ADDR              = os.Getenv("TERRA_RPC")
	TENDERMINT_URL        = os.Getenv("TENDERMINT_URL")
	FEED_POLLING_INTERVAL = os.Getenv("FEED_POLLING_INTERVAL")
	NODE_POLLING_INTERVAL = os.Getenv("NODE_POLLING_INTERVAL")
)

const (
	ContractType = "flux_monitor"
	NetworkName  = "Terra"
)

func StringToInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

var (
	// Counters
	FmAnswersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_answers_total",
			Help: "Reports the number of on-chain answers for a feed",
		},
		[]string{"contract_address"},
	)
	FmSubmissionsReceivedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_submissions_received_total",
			Help: "Reports the current number of submissions",
		},
		[]string{"contract_address", "sender"},
	)
	FmRoundsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_rounds",
			Help: "The number of rounds the monitor has observed on a feed",
		},
		[]string{"contract_address"},
	)
	// Gauges
	FmSubmissionReceivedValuesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_submission_received_values",
			Help: "Reports the current submission value for an oracle on a feed",
		},
		[]string{"contract_address", "sender"},
	)
	NodeMetadataGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "node_metadata",
			Help: "Exposes metdata for node",
		},
		[]string{"chain_id", "network_name", "oracle", "sender"},
	)
	FeedContractMetadataGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "feed_contract_metadata",
			Help: "Exposes metadata for individual feeds",
		},
		[]string{"chain_id", "contract_address", "contract_status", "contract_type", "feed_name", "feed_path", "network_id", "network_name", "symbol"},
	)
	FmAnswersGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_answers",
			Help: "Reports the current on chain price for a feed.",
		},
		[]string{"contract_address"},
	)
	FmCurrentHeadGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "head_tracker_current_head",
			Help: "Tracks the current block height that the monitoring instance has processed",
		},
		[]string{"network_name", "chain_id"},
	)
	FmLatestRoundResponsesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_latest_round_responses",
			Help: "Reports the current number of submissions received for the latest round for a feed",
		},
		[]string{"contract_address"},
	)
	// TODO:
	FmRoundAge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_round_age",
			Help: "Indicates how many blocks have been mined after a round started where no answer has been produced",
		},
		[]string{"contract_address"},
	)
)

type Exporter struct {
	managers map[string]*manager.FeedManager
	nodes    map[string]*types.NodeConfig

	WasmClient       wasmTypes.QueryClient
	TendermintClient *tmrpc.HTTP

	logger      log.Logger
	msgCh       chan types.Message
	mutex       sync.RWMutex
	kafkaWriter *kafka.Writer

	feedPollingInterval time.Duration
	nodePollingInterval time.Duration

	answersCounter           *prometheus.CounterVec
	answersGauge             *prometheus.GaugeVec
	submissionsCounter       *prometheus.CounterVec
	submissionsGauge         *prometheus.GaugeVec
	roundsCounter            *prometheus.CounterVec
	nodeMetadataGauge        *prometheus.GaugeVec
	contractMetadataGauge    *prometheus.GaugeVec
	currentHeadGauge         *prometheus.GaugeVec
	latestRoundResponseGauge *prometheus.GaugeVec
	roundAgeGauge            *prometheus.GaugeVec
}

func NewWasmClient() (wasmClient wasmTypes.QueryClient, err error) {
	grpcConn, err := grpc.Dial(
		RPC_ADDR,
		grpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	wasmClient = wasmTypes.NewQueryClient(grpcConn)
	return wasmClient, nil
}

func NewTendermintClient() (*tmrpc.HTTP, error) {
	client, err := tmrpc.New(TENDERMINT_URL, "/websocket")
	if err != nil {
		return nil, err
	}
	err = client.Start()
	if err != nil {
		return nil, err
	}

	return client, nil
}

func GetFeedConfig(feeds map[string]types.FeedConfig) error {
	var config []types.FeedConfig
	response, err := http.Get(FEED_CONFIG_URL)
	if err != nil {
		return err
	}

	err = json.NewDecoder(response.Body).Decode(&config)
	defer response.Body.Close()

	if err != nil {
		return err
	}

	for _, feed := range config {
		feeds[feed.ProxyAddress] = feed
	}

	return nil
}

func GetNodeConfig(nodes map[string]*types.NodeConfig) error {
	var config []types.NodeConfig
	response, err := http.Get(NODE_CONFIG_URL)

	if err != nil {
		return err
	}

	err = json.NewDecoder(response.Body).Decode(&config)
	defer response.Body.Close()

	if err != nil {
		return err
	}

	for _, node := range config {
		nodes[node.NodeAddress[0]] = &node
	}

	return nil
}

func NewExporter(
	l log.Logger,
	ch chan types.Message,
	kafka *kafka.Writer,
	feedPollingInterval time.Duration,
	nodePollingInterval time.Duration,
) (*Exporter, error) {
	// create wasm client
	wasmClient, err := NewWasmClient()
	if err != nil {
		level.Error(l).Log("msg", "Could not create wasm client", "err", err)
		return nil, err
	}

	// create tendermint client
	// We use a client here to be able to fetch node data
	// Feed managers have their own tendermint client connections
	// to work around the max subs per client restriction
	tendermintClient, err := NewTendermintClient()
	if err != nil {
		level.Error(l).Log("msg", "Could not create tendermint client", "err", err)
		return nil, err
	}

	// fetch feed configurations
	feeds := make(map[string]types.FeedConfig)
	err = GetFeedConfig(feeds)
	if err != nil {
		return nil, err
	}

	// fetch node configurations
	nodes := make(map[string]*types.NodeConfig)
	err = GetNodeConfig(nodes)
	if err != nil {
		return nil, err
	}

	// create feed managers
	managers := make(map[string]*manager.FeedManager)
	for k, feed := range feeds {
		tendermintClient, err = NewTendermintClient()
		if err != nil {
			level.Error(l).Log("msg", "Could not create tendermint client for manager", "err", err)
			continue
		}
		feedManager := manager.NewManager(feed, tendermintClient, l)
		managers[k] = feedManager
	}

	e := Exporter{
		managers:         managers,
		nodes:            nodes,
		WasmClient:       wasmClient,
		TendermintClient: tendermintClient,
		logger:           l,
		msgCh:            ch,
		mutex:            sync.RWMutex{},
		kafkaWriter:      kafka,

		feedPollingInterval: feedPollingInterval,
		nodePollingInterval: nodePollingInterval,

		answersCounter: FmAnswersTotal,
		answersGauge:   FmAnswersGauge,

		submissionsCounter: FmSubmissionsReceivedTotal,
		submissionsGauge:   FmSubmissionReceivedValuesGauge,

		roundsCounter: FmRoundsCounter,

		nodeMetadataGauge:     NodeMetadataGauge,
		contractMetadataGauge: FeedContractMetadataGauge,

		currentHeadGauge:         FmCurrentHeadGauge,
		latestRoundResponseGauge: FmLatestRoundResponsesGauge,
		roundAgeGauge:            FmRoundAge,
	}

	// subscribe to feeds events
	e.subscribeFeeds(e.msgCh, e.logger)
	e.pollChanges()
	e.storeEvents(e.msgCh)

	return &e, nil
}

func (e *Exporter) subscribeFeed(ch chan types.Message, logger log.Logger, manager *manager.FeedManager) error {
	aggregator, err := manager.GetAggregator(e.WasmClient)
	if err != nil {
		level.Error(logger).Log("msg", "Could not get the aggregator address", "err", err)
		return err
	}
	// update the feed
	manager.Feed.Aggregator = *aggregator

	// sub proxy first
	err = manager.Subscribe(ch, logger, manager.Feed.ProxyAddress)
	if err != nil {
		return err
	}

	// sub aggregator
	err = manager.Subscribe(ch, logger, manager.Feed.Aggregator)
	if err != nil {
		return err
	}

	return nil
}

func (e *Exporter) subscribeFeeds(ch chan types.Message, logger log.Logger) {
	for _, manager := range e.managers {
		err := e.subscribeFeed(ch, logger, manager)
		if err != nil {
			level.Error(logger).Log("msg", "Could not subscribe feed", "err", err)
			continue
		}
		// set initial contract metadata
		go e.setContractMetadata(manager.Feed.ProxyAddress)
	}
}

func (e *Exporter) setContractMetadata(proxyAddr string) {
	e.mutex.RLock()
	defer e.mutex.RUnlock()

	status, err := e.TendermintClient.Status(context.TODO())
	if err != nil {
		level.Error(e.logger).Log("msg", "Can't get the node status", "err", err)
		return
	}

	feed := e.managers[proxyAddr].Feed

	e.contractMetadataGauge.WithLabelValues(
		status.NodeInfo.Network,
		feed.Aggregator,
		feed.Status,
		ContractType,
		feed.Name,
		feed.Path,
		status.NodeInfo.Network,
		NetworkName,
		feed.Symbol,
	).Set(1)
}

func (e *Exporter) fetchNodeMetrics() {
	status, err := e.TendermintClient.Status(context.TODO())

	if err != nil {
		level.Error(e.logger).Log("msg", "Can't get the node status", "err", err)
		return
	}

	for address, node := range e.nodes {
		e.nodeMetadataGauge.WithLabelValues(
			status.NodeInfo.Network,
			NetworkName,
			node.OracleAddress,
			address,
		).Set(1)
	}

	e.currentHeadGauge.WithLabelValues(
		status.NodeInfo.Network,
		NetworkName,
	).Set(float64(status.SyncInfo.LatestBlockHeight))
}

func (e *Exporter) writeToKafka(key string, value interface{}) error {
	if e.kafkaWriter != nil {
		message, err := json.Marshal(value)
		if err != nil {
			return err
		}
		err = e.kafkaWriter.WriteMessages(context.Background(),
			kafka.Message{
				Key:   []byte(key),
				Value: message,
			},
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func (e *Exporter) storeEvents(out chan types.Message) {
	handler := func(event types.EventRecords) {
		for _, round := range event.NewRound {
			err := e.writeToKafka("NewRound", round)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not send message to kafka", "err", err)
				continue
			}

			level.Info(e.logger).Log("msg", "Got New Round event",
				"round", round.RoundId,
				"Feed", round.Feed)

			e.roundsCounter.With(prometheus.Labels{
				"contract_address": round.Feed,
			}).Inc()

			e.latestRoundResponseGauge.With(prometheus.Labels{
				"contract_address": round.Feed,
			}).Set(float64(0))
		}
		for _, round := range event.SubmissionReceived {
			err := e.writeToKafka("SubmissionReceived", round)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not send message to kafka", "err", err)
				continue
			}
			level.Info(e.logger).Log("msg", "Got Submission Received event",
				"feed", round.Feed,
				"round id", round.RoundId,
				"submission", round.Submission.Key.Int64())

			e.submissionsCounter.With(prometheus.Labels{
				"contract_address": round.Feed,
				"sender":           string(round.Sender),
			}).Inc()

			e.submissionsGauge.With(prometheus.Labels{
				"contract_address": round.Feed,
				"sender":           string(round.Sender),
			}).Set(float64(round.Submission.Key.Int64()))

			e.latestRoundResponseGauge.With(prometheus.Labels{
				"contract_address": round.Feed,
			}).Inc()

		}
		for _, update := range event.AnswerUpdated {
			err := e.writeToKafka("AnswerUpdated", update)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not send message to kafka", "err", err)
				continue
			}

			level.Info(e.logger).Log("msg", "Got Answer Updated event",
				"feed", update.Feed,
				"round", update.RoundId,
				"Answer", update.Value.Key.Int64())

			e.answersCounter.With(prometheus.Labels{
				"contract_address": update.Feed,
			}).Inc()
			e.answersGauge.WithLabelValues(update.Feed).Set(float64(update.Value.Key.Int64()))
		}
		e.mutex.Lock()
		for _, confirm := range event.ConfirmAggregator {
			feed := e.managers[confirm.Feed].Feed
			// This event indicates that the aggregator address for a feed has changed
			// So we need to unsubscribe from old aggregator's events
			err := e.managers[confirm.Feed].Unsubscribe(e.logger, feed.Aggregator)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not unsubscribe old aggregator", "err", err)
				continue
			}

			// Update the feed's aggregator
			feed.Aggregator = confirm.NewAggregator
			e.managers[confirm.Feed].Feed = feed

			// And subscribe to new aggregator's events
			err = e.managers[confirm.Feed].Subscribe(e.msgCh, e.logger, confirm.NewAggregator)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not subscribe to new aggregator", "err", err)
				continue
			}
			level.Info(e.logger).Log("msg", "Got aggregator confirmed event",
				"feed", confirm.Feed,
				"new aggregator", confirm.NewAggregator)

		}
		e.mutex.Unlock()
	}

	go func() {
		for {
			resp, ok := <-out
			if !ok {
				return
			}

			go e.fetchNodeMetrics()

			info := collector.ExtractTxInfo(resp.Event.Data.(tmTypes.EventDataTx))
			eventRecords, err := collector.ParseEvents(
				resp.Event.Data.(tmTypes.EventDataTx).Result.Events,
				info,
				resp.Address,
			)
			if err != nil {
				continue
			}
			if eventRecords != nil {
				handler(*eventRecords)
			}
		}
	}()
}

func (e *Exporter) pollFeeds() {
	newFeeds := make(map[string]types.FeedConfig)
	ticker := time.NewTicker(e.feedPollingInterval)
	for range ticker.C {
		err := GetFeedConfig(newFeeds)
		if err != nil {
			level.Error(e.logger).Log("msg", "Could not fetch new feed configurations", "err", err)
			continue
		}
		for _, feed := range newFeeds {
			e.mutex.Lock()
			_, present := e.managers[feed.ProxyAddress]
			// if the feed is not present in the map of feeds, create new manager and subscribe to events
			if !present {
				level.Info(e.logger).Log("msg", "Found new feed", "name", feed.Name)
				tendermintClient, err := NewTendermintClient()
				if err != nil {
					level.Error(e.logger).Log("msg", "Could not create tendermint client for new Feed", "err", err)
					continue
				}
				manager := manager.NewManager(feed, tendermintClient, e.logger)
				e.managers[feed.ProxyAddress] = manager

				err = e.subscribeFeed(e.msgCh, e.logger, e.managers[feed.ProxyAddress])
				if err != nil {
					continue
				}
			}
			// if the feed exists, update the config
			e.managers[feed.ProxyAddress].UpdateFeed(e.WasmClient, e.logger, feed)
			go e.setContractMetadata(feed.ProxyAddress)
			e.mutex.Unlock()
		}
	}
}

func (e *Exporter) pollNodes() {
	newNodes := make(map[string]*types.NodeConfig)
	ticker := time.NewTicker(e.nodePollingInterval)
	for range ticker.C {
		err := GetNodeConfig(newNodes)
		if err != nil {
			level.Error(e.logger).Log("msg", "Could not fetch new node configurations", "err", err)
			continue
		}

		for _, node := range newNodes {
			e.mutex.Lock()
			_, present := e.nodes[node.NodeAddress[0]]
			if !present {
				level.Info(e.logger).Log("msg", "Found new node", "name", node.Name)
				e.nodes[node.NodeAddress[0]] = node
				continue
			}
			res := cmp.Equal(e.nodes[node.NodeAddress[0]], node)

			if !res {
				e.nodes[node.NodeAddress[0]] = node
			}

			e.mutex.Unlock()
		}
	}
}

func (e *Exporter) pollChanges() {
	go e.pollFeeds()
	go e.pollNodes()
}
