package k8s

import (
	"k8s.io/client-go/dynamic"
)

type K8sClient struct {
	Dynamic dynamic.Interface
}

func NewK8sClient(kubeConfigMode, kubeConfigPath string) (*K8sClient, error) {
	k8sConfig, err := getKubernetesConfig(kubeConfigMode, kubeConfigPath)
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(k8sConfig)
	if err != nil {
		return nil, err
	}

	return &K8sClient{
		Dynamic: dynamicClient,
	}, nil
}
