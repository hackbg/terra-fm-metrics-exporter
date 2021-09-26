package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/hackbg/terra-chainlink-exporter/types"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

var CONFIG_URL = os.Getenv("CONFIG_URL")

type FeedManager struct {
	WasmClient       wasmTypes.QueryClient
	TendermintClient *tmrpc.HTTP
	Feeds            map[string]types.Feed
}

func getConfig(feeds map[string]types.Feed) error {
	var config []types.Feed
	response, err := http.Get(CONFIG_URL)
	if err != nil {
		return err
	}

	err = json.NewDecoder(response.Body).Decode(&config)
	defer response.Body.Close()

	if err != nil {
		return err
	}

	for _, feed := range config {
		feeds[feed.ContractAddress] = feed
	}

	return nil
}

func NewManager(tendermint_url, terra_rpc string) (*FeedManager, error) {
	feeds := make(map[string]types.Feed)
	grpcConn, err := grpc.Dial(
		terra_rpc,
		grpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	client, err := tmrpc.New(fmt.Sprintf("http://%s", tendermint_url), "/websocket")

	if err != nil {
		return nil, err
	}
	err = client.Start()
	if err != nil {
		return nil, err
	}

	err = getConfig(feeds)

	if err != nil {
		return nil, err
	}

	return &FeedManager{
		WasmClient:       wasmTypes.NewQueryClient(grpcConn),
		TendermintClient: client,
		Feeds:            feeds,
	}, nil
}

func (fm *FeedManager) Subscribe(address string, msgs chan types.Message, logger log.Logger) (err error) {
	level.Info(logger).Log("msg", "Subscribing to feed", "address", address)
	query := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", address)
	out, err := fm.TendermintClient.Subscribe(context.Background(), "subscribe", query)

	if err != nil {
		return err
	}

	go func() {
		for {
			resp, ok := <-out
			if !ok {
				return
			}
			msg := types.Message{Event: resp, Address: address}
			msgs <- msg
		}
	}()

	return nil
}

func (fm *FeedManager) Unsubscribe(address string, logger log.Logger) error {
	level.Info(logger).Log("msg", "Unsubscribing from feed", "address", address)
	query := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", address)
	return fm.TendermintClient.Unsubscribe(context.Background(), "unsubscribe", query)
}

func (fm *FeedManager) getAggregator(proxyAddress string) (aggregator *string, err error) {
	res, err := fm.WasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: proxyAddress,
			QueryMsg:        []byte(`{"get_aggregator": {}}`),
		},
	)

	if err != nil {
		return nil, err
	}

	var aggregatorAddress string
	err = json.Unmarshal(res.QueryResult, &aggregatorAddress)

	if err != nil {
		return nil, err
	}

	return &aggregatorAddress, nil
}

func (fm *FeedManager) InitializeFeeds(ch chan types.Message, logger log.Logger) error {
	for _, feed := range fm.Feeds {
		aggregator, err := fm.getAggregator(feed.ContractAddress)
		if err != nil {
			level.Error(logger).Log("msg", "Could not get the aggregator address", "err", err)
			return err
		}
		// update the feed
		feed.Aggregator = *aggregator
		fm.Feeds[feed.ContractAddress] = feed

		err = fm.Subscribe(*aggregator, ch, logger)

		if err != nil {
			level.Error(logger).Log("msg", "Can't subscribe to address", "err", err)
			return err
		}

	}
	return nil
}

func (fm *FeedManager) Poll(msgs chan types.Message, mu *sync.Mutex, logger log.Logger) {
	newFeeds := make(map[string]types.Feed)
	ticker := time.NewTicker(5 * time.Second)

	for range ticker.C {
		err := getConfig(newFeeds)
		if err != nil {
			continue
		}
		for _, feed := range newFeeds {
			mu.Lock()
			fm.updateFeed(feed, msgs, logger)
			mu.Unlock()
		}
	}
}

func (fm *FeedManager) updateFeed(feed types.Feed, msgs chan types.Message, logger log.Logger) {
	_, present := fm.Feeds[feed.ContractAddress]
	aggregator, err := fm.getAggregator(feed.ContractAddress)

	if err != nil {
		level.Error(logger).Log("msg", "Could not get the aggregator address", "err", err)
		return
	}
	// if the proxy is not present in the list of feeds, create a new feed and subscribe to events
	if !present {
		// Add new feed
		feed.Aggregator = *aggregator
		fm.Feeds[feed.ContractAddress] = feed

		err = fm.Subscribe(*aggregator, msgs, logger)
		if err != nil {
			level.Error(logger).Log("msg", "Could not subscribe to feed", "err", err)
			return
		}
	}

	// Check if any of feed configurations have changed ignoring the Aggregator field
	res := cmp.Equal(fm.Feeds[feed.ContractAddress], feed, cmpopts.IgnoreFields(feed, "Aggregator"))

	// if either changed we need to update and resubscribe
	if !res {
		level.Info(logger).Log("msg", "Feed configuration has changed")
		feed.Aggregator = *aggregator
		fm.Feeds[feed.ContractAddress] = feed
	}
}