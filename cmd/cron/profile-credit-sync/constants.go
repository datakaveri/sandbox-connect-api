package main

import (
	"sandbox-backend-service/pkg/utils"
	"time"
)

const (
	opencostTimeout        = 5 * time.Minute
	keycloakTokenTimeout   = 30 * time.Second
	creditDeductionTimeout = 30 * time.Second
)

const (
	externalApiInitialDelay = 3 * time.Second
	externalApiMaxDelay     = 30 * time.Second
	externalApiMaxAttempts  = 5
	externalApiFactor       = 2.0

	K8sInitialDelay = 5 * time.Second
	K8sMaxDelay     = 30 * time.Second
	K8sMaxAttempts  = 5
	K8sFactor       = 2.0

	DBInitialDelay = 5 * time.Second
	DBMaxDelay     = 30 * time.Second
	DBMaxAttempts  = 5
	DBFactor       = 2.0
)

var externalApiRetryConfig = utils.BackoffConfig{
	InitialDelay: externalApiInitialDelay,
	MaxDelay:     externalApiMaxDelay,
	MaxAttempts:  externalApiMaxAttempts,
	Factor:       externalApiFactor,
}

var k8sRetryConfig = utils.BackoffConfig{
	InitialDelay: K8sInitialDelay,
	MaxDelay:     K8sMaxDelay,
	MaxAttempts:  K8sMaxAttempts,
	Factor:       K8sFactor,
}

var dbRetryConfig = utils.BackoffConfig{
	InitialDelay: DBInitialDelay,
	MaxDelay:     DBMaxDelay,
	MaxAttempts:  DBMaxAttempts,
	Factor:       DBFactor,
}
