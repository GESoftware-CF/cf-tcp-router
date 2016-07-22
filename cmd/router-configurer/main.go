package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/cf-tcp-router/config"
	"github.com/cloudfoundry-incubator/cf-tcp-router/configurer"
	"github.com/cloudfoundry-incubator/cf-tcp-router/configurer/haproxy"
	"github.com/cloudfoundry-incubator/cf-tcp-router/metrics_reporter"
	"github.com/cloudfoundry-incubator/cf-tcp-router/metrics_reporter/haproxy_client"
	"github.com/cloudfoundry-incubator/cf-tcp-router/models"
	"github.com/cloudfoundry-incubator/cf-tcp-router/routing_table"
	"github.com/cloudfoundry-incubator/cf-tcp-router/syncer"
	"github.com/cloudfoundry-incubator/cf-tcp-router/watcher"
	"github.com/cloudfoundry-incubator/routing-api"
	uaaclient "github.com/cloudfoundry-incubator/uaa-go-client"
	uaaconfig "github.com/cloudfoundry-incubator/uaa-go-client/config"
	"github.com/cloudfoundry/dropsonde"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"
)

const (
	defaultTokenFetchRetryInterval = 5 * time.Second
	defaultTokenFetchNumRetries    = uint(3)
)

var tcpLoadBalancer = flag.String(
	"tcpLoadBalancer",
	configurer.HaProxyConfigurer,
	"The tcp load balancer to use.",
)

var tcpLoadBalancerBaseCfg = flag.String(
	"tcpLoadBalancerBaseConfig",
	"",
	"The tcp load balancer base configuration file name. This contains the basic header information.",
)

var tcpLoadBalancerCfg = flag.String(
	"tcpLoadBalancerConfig",
	"",
	"The tcp load balancer configuration file name.",
)

var tcpLoadBalancerStatsUnixSocket = flag.String(
	"tcpLoadBalancerStatsUnixSocket",
	"/var/vcap/jobs/haproxy/config/haproxy.sock",
	"Unix domain socket for tcp load balancer",
)

var subscriptionRetryInterval = flag.Int(
	"subscriptionRetryInterval",
	5,
	"Retry interval between retries to subscribe for tcp events from routing api (in seconds)",
)

var configFile = flag.String(
	"config",
	"/var/vcap/jobs/router_configurer/config/router_configurer.yml",
	"The Router configurer yml config.",
)

var haproxyReloader = flag.String(
	"haproxyReloader",
	"/var/vcap/jobs/router_configurer/bin/haproxy_reloader",
	"Path to a script that reloads HAProxy.",
)

var syncInterval = flag.Duration(
	"syncInterval",
	time.Minute,
	"The interval between syncs of the routing table from routing api.",
)

var tokenFetchMaxRetries = flag.Uint(
	"tokenFetchMaxRetries",
	defaultTokenFetchNumRetries,
	"Maximum number of retries the Token Fetcher will use every time FetchToken is called",
)

var tokenFetchRetryInterval = flag.Duration(
	"tokenFetchRetryInterval",
	defaultTokenFetchRetryInterval,
	"interval to wait before TokenFetcher retries to fetch a token",
)

var tokenFetchExpirationBufferTime = flag.Uint64(
	"tokenFetchExpirationBufferTime",
	30,
	"Buffer time in seconds before the actual token expiration time, when TokenFetcher consider a token expired",
)

var statsCollectionInterval = flag.Duration(
	"statsCollectionInterval",
	time.Minute,
	"The interval between collection of stats from tcp load balancer.",
)

var dropsondePort = flag.Int(
	"dropsondePort",
	3457,
	"Port the local metron agent is listening on",
)

var staleRouteCheckInterval = flag.Duration(
	"staleRouteCheckInterval",
	30*time.Second,
	"The interval at which router checks for expired routes",
)

var defaultRouteExpiry = flag.Duration(
	"defaultRouteExpiry",
	2*time.Minute,
	"The default ttl for a route",
)

const (
	dropsondeOrigin        = "router-configurer"
	statsConnectionTimeout = 10 * time.Second
)

func main() {
	cf_debug_server.AddFlags(flag.CommandLine)
	cf_lager.AddFlags(flag.CommandLine)
	flag.Parse()

	logger, reconfigurableSink := cf_lager.New("router-configurer")
	logger.Info("starting")
	clock := clock.NewClock()

	initializeDropsonde(logger)

	routingTable := models.NewRoutingTable(logger)
	reloaderRunner := haproxy.CreateCommandRunner(*haproxyReloader, logger)
	configurer := configurer.NewConfigurer(
		logger,
		*tcpLoadBalancer,
		*tcpLoadBalancerBaseCfg,
		*tcpLoadBalancerCfg,
		reloaderRunner,
	)

	cfg, err := config.New(*configFile)
	if err != nil {
		logger.Error("failed-to-unmarshal-config-file", err)
		os.Exit(1)
	}

	if defaultRouteExpiry.Seconds() > 65535 {
		logger.Error("invalid-route-expiry", errors.New("route expiry cannot be greater than 65535"))
		os.Exit(1)
	}

	if staleRouteCheckInterval.Seconds() > defaultRouteExpiry.Seconds() {
		logger.Error("invalid-stale-route-check-interval", errors.New("stale route check interval cannot be greater than route expiry"))
		os.Exit(1)
	}

	uaaClient := newUaaClient(logger, cfg, clock)
	_, err = uaaClient.FetchToken(false)
	if err != nil {
		logger.Error("error-fetching-oauth-token", err)
		os.Exit(1)
	}

	routingAPIAddress := fmt.Sprintf("%s:%d", cfg.RoutingAPI.URI, cfg.RoutingAPI.Port)
	logger.Debug("creating-routing-api-client", lager.Data{"api-location": routingAPIAddress})
	routingAPIClient := routing_api.NewClient(routingAPIAddress, false)

	updater := routing_table.NewUpdater(logger, &routingTable, configurer, routingAPIClient, uaaClient, clock, int(defaultRouteExpiry.Seconds()))

	ticker := clock.NewTicker(*staleRouteCheckInterval)

	go startRoutePruner(ticker, updater)

	syncChannel := make(chan struct{})
	syncRunner := syncer.New(clock, *syncInterval, syncChannel, logger)
	watcher := watcher.New(routingAPIClient, updater, uaaClient, *subscriptionRetryInterval, syncChannel, logger)

	haproxyClient := haproxy_client.NewClient(logger, *tcpLoadBalancerStatsUnixSocket, statsConnectionTimeout)
	metricsEmitter := metrics_reporter.NewMetricsEmitter()
	metricsReporter := metrics_reporter.NewMetricsReporter(clock, haproxyClient, metricsEmitter, *statsCollectionInterval)

	members := grouper.Members{
		{"watcher", watcher},
		{"syncer", syncRunner},
		{"metricsReporter", metricsReporter},
	}

	if dbgAddr := cf_debug_server.DebugAddress(flag.CommandLine); dbgAddr != "" {
		members = append(grouper.Members{
			{"debug-server", cf_debug_server.Runner(dbgAddr, reconfigurableSink)},
		}, members...)
	}

	group := grouper.NewOrdered(os.Interrupt, members)

	monitor := ifrit.Invoke(sigmon.New(group))

	logger.Info("started")

	err = <-monitor.Wait()
	if err != nil {
		logger.Error("exited-with-failure", err)
		os.Exit(1)
	}

	logger.Info("exited")
}

func startRoutePruner(ticker clock.Ticker, updater routing_table.Updater) {
	for {
		select {
		case <-ticker.C():
			updater.PruneStaleRoutes()
		}
	}
}

func newUaaClient(logger lager.Logger, c *config.Config, klok clock.Clock) uaaclient.Client {
	if c.RoutingAPI.AuthDisabled {
		logger.Debug("creating-noop-uaa-client")
		client := uaaclient.NewNoOpUaaClient()
		return client
	}
	logger.Debug("creating-uaa-client")

	if c.OAuth.Port == -1 {
		logger.Fatal("tls-not-enabled", errors.New("TcpRouter requires to communicate with UAA over TLS"), lager.Data{"token-endpoint": c.OAuth.TokenEndpoint, "port": c.OAuth.Port})
	}

	tokenURL := fmt.Sprintf("https://%s:%d", c.OAuth.TokenEndpoint, c.OAuth.Port)

	cfg := &uaaconfig.Config{
		UaaEndpoint:           tokenURL,
		SkipVerification:      c.OAuth.SkipSSLValidation,
		ClientName:            c.OAuth.ClientName,
		ClientSecret:          c.OAuth.ClientSecret,
		MaxNumberOfRetries:    uint32(*tokenFetchMaxRetries),
		RetryInterval:         *tokenFetchRetryInterval,
		ExpirationBufferInSec: int64(*tokenFetchExpirationBufferTime),
		CACerts:               c.OAuth.CACerts,
	}

	uaaClient, err := uaaclient.NewClient(logger, cfg, klok)
	if err != nil {
		logger.Fatal("initialize-token-fetcher-error", err)
	}
	return uaaClient
}

func initializeDropsonde(logger lager.Logger) {
	dropsondeDestination := fmt.Sprintf("localhost:%d", *dropsondePort)
	err := dropsonde.Initialize(dropsondeDestination, dropsondeOrigin)
	if err != nil {
		logger.Error("failed-to-initialize-dropsonde", err)
	}
}
