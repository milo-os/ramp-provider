// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"flag"
	"os"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"go.miloapis.com/ramp-provider/internal/ramp"
	"go.miloapis.com/ramp-provider/internal/syncer"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var platformKubeconfig string
	var leaderElectionNamespace string

	var rampEndpoint string
	var rampTokenURL string
	var rampClientID string
	var rampClientSecret string
	var rampClientIDFile string
	var rampClientSecretFile string
	var resyncInterval time.Duration

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&platformKubeconfig, "milo-kubeconfig", "",
		"Path to a kubeconfig file for the Milo API server. "+
			"If empty, the in-cluster client is used.")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "",
		"Namespace where the leader election Lease is stored. When --milo-kubeconfig "+
			"is set, the controller pod's own namespace usually does not exist on Milo; "+
			"point this at a namespace that does (e.g. milo-system). If empty, "+
			"controller-runtime's default detection is used.")

	flag.StringVar(&rampEndpoint, "ramp-endpoint", ramp.DefaultEndpoint,
		"Base URL of the Ramp API. Override for sandbox or fixtures.")
	flag.StringVar(&rampTokenURL, "ramp-token-url", ramp.DefaultTokenURL,
		"OAuth2 token endpoint URL. Override for sandbox.")
	flag.StringVar(&rampClientID, "ramp-client-id", "",
		"Ramp OAuth2 client_id. Prefer --ramp-client-id-file or RAMP_CLIENT_ID env to avoid leaking via process args.")
	flag.StringVar(&rampClientSecret, "ramp-client-secret", "",
		"Ramp OAuth2 client_secret. Prefer --ramp-client-secret-file or RAMP_CLIENT_SECRET env to avoid leaking via process args.")
	flag.StringVar(&rampClientIDFile, "ramp-client-id-file", "",
		"Path to a file containing the Ramp OAuth2 client_id. Typically a key from a mounted Secret.")
	flag.StringVar(&rampClientSecretFile, "ramp-client-secret-file", "",
		"Path to a file containing the Ramp OAuth2 client_secret. Typically a key from a mounted Secret.")
	flag.DurationVar(&resyncInterval, "resync-interval", 24*time.Hour,
		"How often to re-list vendors from Ramp. The first sync runs immediately on startup.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	clientID, err := resolveCredential(rampClientID, rampClientIDFile, "RAMP_CLIENT_ID")
	if err != nil {
		setupLog.Error(err, "resolving ramp client_id")
		os.Exit(1)
	}
	clientSecret, err := resolveCredential(rampClientSecret, rampClientSecretFile, "RAMP_CLIENT_SECRET")
	if err != nil {
		setupLog.Error(err, "resolving ramp client_secret")
		os.Exit(1)
	}
	if clientID == "" || clientSecret == "" {
		setupLog.Error(nil,
			"ramp client_id and client_secret are required; "+
				"set via --ramp-client-id(-file), --ramp-client-secret(-file), "+
				"or RAMP_CLIENT_ID / RAMP_CLIENT_SECRET env vars")
		os.Exit(1)
	}

	rampClient, err := ramp.NewClient(ramp.Config{
		Endpoint:     rampEndpoint,
		TokenURL:     rampTokenURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})
	if err != nil {
		setupLog.Error(err, "building ramp client")
		os.Exit(1)
	}

	// Build the REST config. When --milo-kubeconfig is provided the manager
	// talks directly to the Milo aggregated API server (where the Vendor
	// CRDs are installed). Otherwise fall back to the in-cluster config for
	// local development. The leader-election Lease lives on the same
	// cluster — Milo when wired up that way, otherwise the host cluster.
	var restCfg *rest.Config
	if platformKubeconfig != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", platformKubeconfig)
		if err != nil {
			setupLog.Error(err, "unable to build milo kubeconfig", "path", platformKubeconfig)
			os.Exit(1)
		}
		setupLog.Info("using milo kubeconfig", "path", platformKubeconfig)
	} else {
		restCfg = ctrl.GetConfigOrDie()
		setupLog.Info("using in-cluster config")
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "ramp-provider.compliance.miloapis.com",
		LeaderElectionNamespace: leaderElectionNamespace,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	rampSyncer, err := syncer.New(syncer.Config{
		Client:   mgr.GetClient(),
		Ramp:     rampClient,
		Interval: resyncInterval,
		Log:      ctrl.Log,
	})
	if err != nil {
		setupLog.Error(err, "building ramp syncer")
		os.Exit(1)
	}
	if err := mgr.Add(rampSyncer); err != nil {
		setupLog.Error(err, "registering ramp syncer with manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// resolveCredential picks up a secret value from, in order: an explicit
// flag value, a file path (typically a mounted Secret key), or an env var.
// File contents are trimmed of trailing whitespace.
func resolveCredential(literal, path, envVar string) (string, error) {
	if literal != "" {
		return literal, nil
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(data), " \t\r\n"), nil
	}
	return os.Getenv(envVar), nil
}
