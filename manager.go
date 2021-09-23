package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/go-cmp/cmp"
	"github.com/hackbg/terra-chainlink-exporter/types"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

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
	var feeds = make(map[string]types.Feed)
	grpcConn, err := grpc.Dial(
		terra_rpc,
		grpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	client, err := tmrpc.New(fmt.Sprintf("http://%s", TENDERMINT_URL), "/websocket")

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

func (fm *FeedManager) subscribe(address string) (out <-chan ctypes.ResultEvent, err error) {
	query := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", address)
	return fm.TendermintClient.Subscribe(context.Background(), "subscribe", query)
}

func (fm *FeedManager) unsubscribe(address string) error {
	query := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", address)
	return fm.TendermintClient.Unsubscribe(context.Background(), "unsubscribe", query)
}

func (fm *FeedManager) initializeFeeds(ch chan types.Message, logger log.Logger) error {
	for _, feed := range fm.Feeds {
		aggregator, err := fm.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: feed.ContractAddress,
				QueryMsg:        []byte(`{"get_aggregator": {}}`),
			},
		)

		if err != nil {
			return err
		}

		var aggregatorAddress string
		err = json.Unmarshal(aggregator.QueryResult, &aggregatorAddress)

		if err != nil {
			return err
		}
		go func() {
			level.Info(logger).Log("msg", "Subscribing to address", "address", aggregatorAddress)
			out, err := fm.subscribe(aggregatorAddress)

			if err != nil {
				level.Error(logger).Log("msg", "Can't parse aggregator address", "err", err)
			}

			for {
				resp, ok := <-out
				if !ok {
					return
				}
				msg := types.Message{Event: resp, Address: aggregatorAddress}
				ch <- msg
			}
		}()
	}
	return nil
}

func (fm *FeedManager) poll(msgs chan types.Message, mu *sync.Mutex) {
	var newFeeds = make(map[string]types.Feed)
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {

		err := getConfig(newFeeds)

		if err != nil {
			continue
		}

		for _, feed := range newFeeds {
			mu.Lock()
			fm.updateFeed(feed, msgs)
			mu.Unlock()
		}
	}
}

func (fm *FeedManager) updateFeed(feed types.Feed, msgs chan types.Message) {
	_, present := fm.Feeds[feed.ContractAddress]

	// if the proxy is not present in the list of feeds, create a new feed and subscribe to events
	if !present {
		fm.Feeds[feed.ContractAddress] = feed
		// TODO: sub / unsub
	}

	res := cmp.Equal(fm.Feeds[feed.ContractAddress], feed)

	// check if the feed parameters have changed
	if !res {
		fm.Feeds[feed.ContractAddress] = feed

		// if feed.Status != "live" {
		// 	// TODO: unsub proxy??
		// }
	}
}
