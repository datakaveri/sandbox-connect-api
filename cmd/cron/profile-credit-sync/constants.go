package main

import (
	"sandbox-backend-service/pkg/utils"
	"time"
)

const (
	opencostTimeout        = 30 * time.Minute
	keycloakTokenTimeout   = 30 * time.Second
	creditDeductionTimeout = 30 * time.Second
)

const (
	externalApiInitialDelay = 1 * time.Microsecond
	externalApiMaxDelay     = 10 * time.Second
	externalApiMaxAttempts  = 5
	externalApiFactor       = 2.0

	K8sInitialDelay = 500 * time.Millisecond
	K8sMaxDelay     = 5 * time.Second
	K8sMaxAttempts  = 5
	K8sFactor       = 2.0

	DBInitialDelay = 200 * time.Millisecond
	DBMaxDelay     = 5 * time.Second
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
