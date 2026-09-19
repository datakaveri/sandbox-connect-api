package k8s

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

type K8sClient struct {
	Dynamic dynamic.Interface
	Core    kubernetes.Interface
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
	coreClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, err
	}

	return &K8sClient{
		Dynamic: dynamicClient,
		Core:    coreClient,
	}, nil
}
