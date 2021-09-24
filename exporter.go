package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/hackbg/terra-chainlink-exporter/collector"
	"github.com/hackbg/terra-chainlink-exporter/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	tmTypes "github.com/tendermint/tendermint/types"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
)

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

func newKafkaWriter(kafkaURL, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:     kafka.TCP(kafkaURL),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
}

var (
	// Counters
	// DONE
	fmAnswersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_answers_total",
			Help: "Reports the number of on-chain answers for a feed",
		},
		[]string{"contract_address"},
	)
	// DONE
	fmSubmissionsReceivedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_submissions_received_total",
			Help: "Reports the current number of submissions",
		},
		[]string{"contract_address", "sender"},
	)
	// DONE
	fmRoundsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_rounds",
			Help: "The number of rounds the monitor has observed on a feed",
		},
		[]string{"contract_address"},
	)
	// Gauges
	// DONE
	fmSubmissionReceivedValuesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_submission_received_values",
			Help: "Reports the current submission value for an oracle on a feed",
		},
		[]string{"contract_address", "sender"},
	)
	nodeMetadataGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "node_metadata",
			Help: "Exposes metdata for node",
		},
		[]string{"chain_id", "network_name", "oracle", "sender"},
	)
	// DONE
	feedContractMetadataGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "feed_contract_metadata",
			Help: "Exposes metadata for individual feeds",
		},
		[]string{"chain_id", "contract_address", "contract_status", "contract_type", "feed_name", "feed_path", "network_id", "network_name", "symbol"},
	)
	// DONE
	fmAnswersGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_answers",
			Help: "Reports the current on chain price for a feed.",
		},
		[]string{"contract_address"},
	)
	// DONE
	fmCurrentHeadGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "head_tracker_current_head",
			Help: "Tracks the current block height that the monitoring instance has processed",
		},
		[]string{"network_name", "chain_id"},
	)
	// TODO:
	fmLatestRoundResponsesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_latest_round_responses",
			Help: "Reports the current number of submissions received for the latest round for a feed",
		},
		[]string{"contract_address"},
	)
	// TODO:
	fmRoundAge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_round_age",
			Help: "Indicates how many blocks have been mined after a round started where no answer has been produced",
		},
		[]string{"contract_address"},
	)
)

type Exporter struct {
	feedManager FeedManager
	logger      log.Logger
	msgCh       chan types.Message
	mutex       sync.Mutex
	kafkaWriter *kafka.Writer

	answersCounter          *prometheus.CounterVec
	answersGauge            *prometheus.GaugeVec
	submissionsCounter      *prometheus.CounterVec
	submissionsGauge        *prometheus.GaugeVec
	roundsCounter           *prometheus.CounterVec
	nodeMetadataGauge       *prometheus.GaugeVec
	contractMetadataGauge   *prometheus.GaugeVec
	currentHeadGauge        *prometheus.GaugeVec
	latetRoundResponseGauge *prometheus.GaugeVec
	roundAgeGauge           *prometheus.GaugeVec

	roundsEvents     []types.EventNewRound
	submissionEvents []types.EventSubmissionReceived
	answersEvents    []types.EventAnswerUpdated
}

func NewExporter(fm FeedManager, l log.Logger, ch chan types.Message, kafka *kafka.Writer) *Exporter {
	e := Exporter{
		feedManager: fm,
		logger:      l,
		msgCh:       ch,
		mutex:       sync.Mutex{},
		kafkaWriter: kafka,

		answersCounter: fmAnswersTotal,
		answersGauge:   fmAnswersGauge,

		submissionsCounter: fmSubmissionsReceivedTotal,
		submissionsGauge:   fmSubmissionReceivedValuesGauge,

		roundsCounter: fmRoundsCounter,

		nodeMetadataGauge:     nodeMetadataGauge,
		contractMetadataGauge: feedContractMetadataGauge,

		currentHeadGauge:        fmCurrentHeadGauge,
		latetRoundResponseGauge: fmLatestRoundResponsesGauge,
		roundAgeGauge:           fmRoundAge,

		roundsEvents:     []types.EventNewRound{},
		submissionEvents: []types.EventSubmissionReceived{},
		answersEvents:    []types.EventAnswerUpdated{},
	}

	e.feedManager.initializeFeeds(e.msgCh, e.logger)
	e.pollChanges()
	e.consume(e.msgCh)

	return &e
}

// Describe describes all the metrics ever exporter (not including ones that should read from ws) by the Terra Chainlink exporter.
// It implements prometheus.Collector
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	e.contractMetadataGauge.Describe(ch)
	e.answersGauge.Describe(ch)
	e.currentHeadGauge.Describe(ch)
}

// Collect fetches the metrics
// It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mutex.Lock()

	e.CollectLatestRoundData(ch)
	e.CollectLatestBlockHeight(ch)
	e.CollectAggregatorConfig(ch)
	e.CollectRoundMetrics(ch)
	e.CollectSubmissionMetrics(ch)
	e.CollectAnswerMetrics(ch)

	e.mutex.Unlock()
}

func (e *Exporter) CollectRoundMetrics(ch chan<- prometheus.Metric) bool {
	// aggregate the metrics
	for _, round := range e.roundsEvents {
		e.roundsCounter.With(prometheus.Labels{
			"contract_address": round.Feed,
		}).Inc()
		e.answersCounter.Collect(ch)
	}
	// reset
	e.roundsEvents = nil

	return true
}

func (e *Exporter) CollectSubmissionMetrics(ch chan<- prometheus.Metric) bool {
	for _, submission := range e.submissionEvents {
		fmt.Printf("Submission: %+v\n", submission)
		e.submissionsCounter.With(prometheus.Labels{
			"contract_address": submission.Feed,
			"sender":           string(submission.Sender),
		}).Inc()
		e.submissionsCounter.Collect(ch)

		e.submissionsGauge.With(prometheus.Labels{
			"contract_address": submission.Feed,
			"sender":           string(submission.Sender),
		}).Set(float64(submission.Submission.Key.Int64()))
		e.submissionsGauge.Collect(ch)
	}
	e.submissionEvents = nil

	return true
}

func (e *Exporter) CollectAnswerMetrics(ch chan<- prometheus.Metric) bool {
	for _, answer := range e.answersEvents {
		e.answersCounter.With(prometheus.Labels{
			"contract_address": answer.Feed,
		}).Inc()
		e.answersCounter.Collect(ch)
	}
	e.answersEvents = nil

	return true
}

func (e *Exporter) CollectLatestRoundData(ch chan<- prometheus.Metric) bool {
	for _, feed := range e.feedManager.Feeds {
		response, err := e.feedManager.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: feed.ContractAddress,
				QueryMsg:        []byte(`{"aggregator_query": {"get_latest_round_data": {}}}`),
			},
		)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't query the chain", "err", err)
			return false
		}

		var res LatestRoundResponse
		err = json.Unmarshal(response.QueryResult, &res)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't parse latest round response", "err", err)
			return false
		}
		e.answersGauge.WithLabelValues(feed.ContractAddress).Set(float64(StringToInt(res.Answer)))
		e.answersGauge.Collect(ch)
		e.answersGauge.Reset()
	}

	return true
}

func (e *Exporter) CollectLatestBlockHeight(ch chan<- prometheus.Metric) bool {
	status, err := e.feedManager.TendermintClient.Status(context.Background())
	if err != nil {
		level.Error(e.logger).Log("msg", "Can't get the latest block height", "err", err)
		return false
	}

	level.Info(e.logger).Log("network", status.NodeInfo.Network)

	e.currentHeadGauge.WithLabelValues(status.NodeInfo.Network, status.NodeInfo.Moniker).Set(float64(status.SyncInfo.LatestBlockHeight))
	e.currentHeadGauge.Collect(ch)
	e.currentHeadGauge.Reset()

	// TODO:
	e.nodeMetadataGauge.WithLabelValues(status.NodeInfo.Network, status.NodeInfo.Moniker, "placeholder", "placeholder").Set(1)
	e.nodeMetadataGauge.Collect(ch)
	e.nodeMetadataGauge.Reset()

	return true
}

func (e *Exporter) CollectAggregatorConfig(ch chan<- prometheus.Metric) bool {
	status, err := e.feedManager.TendermintClient.Status(context.Background())
	if err != nil {
		level.Error(e.logger).Log("msg", "Can't get the latest block height", "err", err)
		return false
	}
	for _, feed := range e.feedManager.Feeds {
		aggregator, err := e.feedManager.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: feed.ContractAddress,
				QueryMsg:        []byte(`{"get_aggregator": {}}`),
			},
		)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't query proxy aggregator", "err", err)
			return false
		}

		var aggregatorAddress string
		err = json.Unmarshal(aggregator.QueryResult, &aggregatorAddress)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't parse aggregator address", "err", err)
			return false
		}

		config, err := e.feedManager.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: aggregatorAddress,
				QueryMsg:        []byte(`{"get_aggregator_config": {}}`),
			},
		)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't query aggregator", "err", err)
			return false
		}

		var agggregatorConfig AggregatorConfigResponse
		err = json.Unmarshal(config.QueryResult, &agggregatorConfig)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't parse aggregator config", "err", err)
			return false
		}

		e.contractMetadataGauge.WithLabelValues(
			status.NodeInfo.Network,
			aggregatorAddress,
			feed.Status,
			ContractType,
			feed.Name,
			feed.Path,
			status.NodeInfo.Network,
			status.NodeInfo.Moniker,
			feed.Symbol,
		).Set(1)
		e.contractMetadataGauge.Collect(ch)
		e.contractMetadataGauge.Reset()

	}
	return true
}

func (e *Exporter) consume(out chan types.Message) {
	handler := func(event types.EventRecords) {
		for _, round := range event.NewRound {
			res, err := json.Marshal(round)

			if err != nil {
				level.Error(e.logger).Log("msg", "Could not parse message", "err", err)
				continue
			}

			e.mutex.Lock()
			err = e.kafkaWriter.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("NewRound"),
					Value: res,
				},
			)

			if err != nil {
				level.Error(e.logger).Log("msg", "Could not write kafka message", "err", err)
				continue
			}

			level.Info(e.logger).Log("msg", "Got New Round event: ", "round", round.RoundId, "Feed", round.Feed)

			e.roundsEvents = append(e.roundsEvents, round)
			// e.roundsCounter.With(prometheus.Labels{
			// 	"contract_address": round.Feed,
			// }).Inc()
			e.mutex.Unlock()

			// fmLatestRoundResponsesGauge.With(prometheus.Labels{
			// 	"contract_address": m.Feed.ContractAddress,
			// }).Set(float64(m.latestRoundInfo.Submissions))
		}
		for _, round := range event.SubmissionReceived {
			res, err := json.Marshal(round)

			if err != nil {
				level.Error(e.logger).Log("msg", "Could not parse message", "err", err)
				continue
			}

			e.mutex.Lock()
			err = e.kafkaWriter.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("NewRound"),
					Value: res,
				},
			)

			if err != nil {
				level.Error(e.logger).Log("msg", "Could not write kafka message", "err", err)
				continue
			}

			level.Info(e.logger).Log("msg", "Got Subbmission Received event", "round id", round.RoundId, "submission", round.Submission.Key.Int64())

			e.submissionEvents = append(e.submissionEvents, round)
			// e.submissionsGauge.With(prometheus.Labels{
			// 	"contract_address": round.Feed,
			// 	"sender":           string(round.Sender),
			// }).Set(float64(round.Submission.Key.Int64()))

			// e.submissionsCounter.With(prometheus.Labels{
			// 	"contract_address": round.Feed,
			// 	"sender":           string(round.Sender),
			// }).Inc()
			e.mutex.Unlock()
		}
		for _, update := range event.AnswerUpdated {
			res, err := json.Marshal(update)

			if err != nil {
				level.Error(e.logger).Log("msg", "Could not parse message", "err", err)
				continue
			}

			e.mutex.Lock()
			err = e.kafkaWriter.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("NewRound"),
					Value: res,
				},
			)

			if err != nil {
				level.Error(e.logger).Log("msg", "Could not write kafka message", "err", err)
				continue
			}
			level.Info(e.logger).Log("msg", "Got Answer Updated event", "round", update.RoundId, "Answer", update.Value.Key.Int64())
			e.answersEvents = append(e.answersEvents, update)
			// e.answersCounter.With(prometheus.Labels{
			// 	"contract_address": update.Feed,
			// }).Inc()
			e.mutex.Unlock()
		}
		// for range event.ConfirmAggregator {
		// 	// subscribe to the new aggregator's events
		// 	go m.resubscribeAggregator(out)
		// }
	}

	go func() {
		for {
			resp, ok := <-out
			if !ok {
				return
			}
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

func (e *Exporter) pollChanges() {
	go e.feedManager.poll(e.msgCh, &e.mutex)
}
