package k8s

import (
	"errors"
	"fmt"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"log/slog"
)

func getKubernetesConfig(configType, configPath string) (*rest.Config, error) {
	switch configType {
	case "local":
		if configPath == "" {
			slog.Warn("no kubeconfig path provided for local configuration, using empty path")
		} else {
			slog.Info("using provided kubeconfig path", "path", configPath)
		}
		return clientcmd.BuildConfigFromFlags("", configPath)
	case "cluster":
		return rest.InClusterConfig()
	default:
		return nil, errors.New(fmt.Sprintf("invalid Kubernetes configuration type: type=%s", configType))
	}
}
