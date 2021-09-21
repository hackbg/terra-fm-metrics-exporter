# Prometheus exporter for Terra contracts

## Configuration
### Environment Variables
| Key                        | Description                                                                                | Example                                                            |
| -------------------------- | ------------------------------------------------------------------------------------------ | ------------------------------------------------------------------ |
| `TERRA_RPC`                | The RPC address of the Terra node                                                          | `terrad:9090`                                                      |
| `TENDERMINT_URL`           | Tendermint Connection URL                                                                  | `terrad:26657`                                                     |
| `KAFKA_SERVER`             | Kafka Connection URL                                                                       | `kafka:9092`                                                       |
| `TOPIC`                    | Kafka Topic that the events are pushed to                                                  | `Terra`                                                            |
| `CONFIG_URL`               | URL to poll for feed configs                                                               | `http://localhost:4000/configs`                                    |
